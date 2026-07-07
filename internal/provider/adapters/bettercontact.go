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

// BetterContact builds an async adapter for the BetterContact waterfall aggregator (docs/03 §7).
//   - Submit: POST {base}/async with {"data":[{first_name,last_name,company,company_domain,
//     linkedin_url}],"enrich_email_address":true,"enrich_phone_number":true} → id (HTTP 201).
//   - Poll: GET {base}/async/{id} until top-level status=="terminated" (any other value = pending;
//     the pending literal is undocumented so we treat only "terminated" as done).
//   - Auth: API key in the "X-API-Key" header, injected at egress.
//   - base default https://app.bettercontact.rocks/api/v2 [doc.bettercontact.rocks].
//
// VERIFIED from the OpenAPI spec: endpoints, X-API-Key, id token, EnrichmentResult.data[] keys
// contact_email_address(+_status)/contact_first_name/contact_last_name/contact_job_title. Phone/
// company output keys are UNDOCUMENTED, so not mapped (ADR-0009). Field names UNVERIFIED (hunter.go).
func BetterContact(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://app.bettercontact.rocks/api/v2"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "bettercontact",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-API-Key", KeyPoolSelector: "bettercontact:default"},
		Policy:       provider.CallPolicy{Timeout: 120 * time.Second, MaxAttempts: 1},
		PollInterval: 20 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 5, ExpectedConfidence: 0.95},
			{Field: domain.FieldEmailStatus, Cost: 5, ExpectedConfidence: 0.95},
			{Field: domain.FieldFirstName, Cost: 5, ExpectedConfidence: 0.97},
			{Field: domain.FieldLastName, Cost: 5, ExpectedConfidence: 0.97},
			{Field: domain.FieldJobTitle, Cost: 5, ExpectedConfidence: 0.90},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			lead := map[string]string{}
			putIf(lead, "first_name", req.Known[domain.FieldFirstName])
			putIf(lead, "last_name", req.Known[domain.FieldLastName])
			putIf(lead, "company", req.Known[domain.FieldCompanyName])
			putIf(lead, "company_domain", req.Known[domain.FieldCompanyDomain])
			putIf(lead, "linkedin_url", req.Known[domain.FieldLinkedInURL])
			body := map[string]any{
				"data":                 []map[string]string{lead},
				"enrich_email_address": true,
				"enrich_phone_number":  true,
			}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/async", bytes.NewReader(b))
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
				return "", domain.NewProviderError("bettercontact", domain.ClassTransient, errNoJobID)
			}
			return p.ID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/async/"+token, nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Status string `json:"status"`
				Data   []struct {
					ContactEmailAddress       string `json:"contact_email_address"`
					ContactEmailAddressStatus string `json:"contact_email_address_status"`
					ContactFirstName          string `json:"contact_first_name"`
					ContactLastName           string `json:"contact_last_name"`
					ContactJobTitle           string `json:"contact_job_title"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if p.Status != "terminated" {
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
			put(domain.FieldWorkEmail, d.ContactEmailAddress, 0.95)
			put(domain.FieldEmailStatus, d.ContactEmailAddressStatus, 0.95)
			put(domain.FieldFirstName, d.ContactFirstName, 0.97)
			put(domain.FieldLastName, d.ContactLastName, 0.97)
			put(domain.FieldJobTitle, d.ContactJobTitle, 0.90)
			return res, true, nil
		},
	}
}
