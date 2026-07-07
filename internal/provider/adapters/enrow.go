package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Enrow builds an async adapter for the Enrow email-finder API (docs/03 §7).
//   - Submit: POST {base}/email/find/single with {fullname,company_domain,company_name} → id.
//   - Poll: GET {base}/email/find/single?id={id} until qualification != "ongoing" (HTTP 202 while
//     ongoing, 200 when done; qualification valid/invalid are terminal).
//   - Auth: API key in the "x-api-key" header, injected at egress.
//   - base default https://api.enrow.io [docs.enrow.io].
//
// VERIFIED from docs (+ webhook example): endpoints, x-api-key auth, id token, top-level email +
// qualification + info.{company_domain,company_name,firstname,lastname,fullname}. Any non-"ongoing"
// qualification is treated as terminal. Field names pinned UNVERIFIED (see hunter.go).
func Enrow(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.enrow.io"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "enrow",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "x-api-key", KeyPoolSelector: "enrow:default"},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 5 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 2, ExpectedConfidence: 0.95},
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyName, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldFirstName, Cost: 2, ExpectedConfidence: 0.85},
			{Field: domain.FieldLastName, Cost: 2, ExpectedConfidence: 0.85},
			{Field: domain.FieldFullName, Cost: 2, ExpectedConfidence: 0.75},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]string{}
			putIf(body, "fullname", req.Known[domain.FieldFullName])
			putIf(body, "company_domain", req.Known[domain.FieldCompanyDomain])
			putIf(body, "company_name", req.Known[domain.FieldCompanyName])
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/email/find/single", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.ID == "" {
				return "", domain.NewProviderError("enrow", domain.ClassTransient, errNoJobID)
			}
			return p.ID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/email/find/single?id="+url.QueryEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Email         string `json:"email"`
				Qualification string `json:"qualification"`
				Info          struct {
					CompanyDomain string `json:"company_domain"`
					CompanyName   string `json:"company_name"`
					FirstName     string `json:"firstname"`
					LastName      string `json:"lastname"`
					FullName      string `json:"fullname"`
				} `json:"info"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if p.Qualification == "ongoing" || p.Qualification == "" {
				return provider.Result{}, false, nil // still processing
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, p.Email, 0.95)
			put(domain.FieldEmailStatus, p.Qualification, 0.90)
			put(domain.FieldCompanyDomain, p.Info.CompanyDomain, 0.90)
			put(domain.FieldCompanyName, p.Info.CompanyName, 0.80)
			put(domain.FieldFirstName, p.Info.FirstName, 0.85)
			put(domain.FieldLastName, p.Info.LastName, 0.85)
			put(domain.FieldFullName, p.Info.FullName, 0.75)
			return res, true, nil
		},
	}
}
