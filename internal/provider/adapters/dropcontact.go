package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Dropcontact builds an async adapter for the Dropcontact enrich API (docs/03 §7) — GDPR-compliant
// compute-and-verify email + firmographics.
//   - Submit: POST {base}/enrich/all with {"data":[{first_name,last_name,full_name,email,company,
//     website,linkedin}]} → request_id.
//   - Poll: GET {base}/enrich/all/{request_id} until success==true. A 200 with success:false
//     ("Request not ready yet") is PENDING — keep polling.
//   - Auth: API key in the "X-Access-Token" header on both hops, injected at egress.
//   - base default https://api.dropcontact.com/v1 [developer.dropcontact.com].
//
// VERIFIED from docs (+ live 401 probe): endpoints, X-Access-Token auth, request_id/success
// envelope, data[0].email[].{email,qualification}. Field names pinned UNVERIFIED (see hunter.go).
// Low-confidence phone fields (mobile_phone/office_phone) are omitted pending a live paid response.
func Dropcontact(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.dropcontact.com/v1"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "dropcontact",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-Access-Token", KeyPoolSelector: "dropcontact:default"},
		Policy:       provider.CallPolicy{Timeout: 120 * time.Second, MaxAttempts: 1},
		PollInterval: 30 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 4, ExpectedConfidence: 0.90},
			{Field: domain.FieldEmailStatus, Cost: 4, ExpectedConfidence: 0.90},
			{Field: domain.FieldFirstName, Cost: 4, ExpectedConfidence: 0.90},
			{Field: domain.FieldLastName, Cost: 4, ExpectedConfidence: 0.90},
			{Field: domain.FieldFullName, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyName, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldLinkedInURL, Cost: 4, ExpectedConfidence: 0.70},
			{Field: domain.FieldJobTitle, Cost: 4, ExpectedConfidence: 0.60},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			contact := map[string]string{}
			putIf(contact, "first_name", req.Known[domain.FieldFirstName])
			putIf(contact, "last_name", req.Known[domain.FieldLastName])
			putIf(contact, "full_name", req.Known[domain.FieldFullName])
			putIf(contact, "email", req.Known[domain.FieldWorkEmail])
			putIf(contact, "company", req.Known[domain.FieldCompanyName])
			putIf(contact, "website", req.Known[domain.FieldCompanyDomain])
			putIf(contact, "linkedin", req.Known[domain.FieldLinkedInURL])
			b, err := json.Marshal(map[string]any{"data": []map[string]string{contact}})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/enrich/all", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.RequestID == "" {
				return "", domain.NewProviderError("dropcontact", domain.ClassTransient, errNoJobID)
			}
			return p.RequestID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/enrich/all/"+token, nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Success bool `json:"success"`
				Data    []struct {
					Email []struct {
						Email         string `json:"email"`
						Qualification string `json:"qualification"`
					} `json:"email"`
					FirstName string `json:"first_name"`
					LastName  string `json:"last_name"`
					FullName  string `json:"full_name"`
					Company   string `json:"company"`
					Website   string `json:"website"`
					LinkedIn  string `json:"linkedin"`
					Job       string `json:"job"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if !p.Success {
				return provider.Result{}, false, nil // still processing
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Data) == 0 {
				return res, true, nil
			}
			d := p.Data[0]
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			if len(d.Email) > 0 {
				put(domain.FieldWorkEmail, d.Email[0].Email, 0.90)
				put(domain.FieldEmailStatus, d.Email[0].Qualification, 0.90)
			}
			put(domain.FieldFirstName, d.FirstName, 0.90)
			put(domain.FieldLastName, d.LastName, 0.90)
			put(domain.FieldFullName, d.FullName, 0.80)
			put(domain.FieldCompanyName, d.Company, 0.85)
			put(domain.FieldCompanyDomain, d.Website, 0.85)
			put(domain.FieldLinkedInURL, d.LinkedIn, 0.70)
			put(domain.FieldJobTitle, d.Job, 0.60)
			return res, true, nil
		},
	}
}
