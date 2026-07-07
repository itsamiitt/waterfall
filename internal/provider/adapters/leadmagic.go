package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// LeadMagic builds an adapter for the LeadMagic v1 Email Finder (docs/03 §7).
//   - Endpoint: POST {base}/people/email-finder with {first_name,last_name,full_name,domain,
//     company_name}  (base default https://api.leadmagic.io/v1) [leadmagic.io/docs/v1].
//   - Auth: API key in the "X-API-Key" header (single credential), injected at egress.
//   - Input: first+last / full_name AND company_domain / company_name. Fills work_email + basic firmo.
//     Pay-on-success: no match = HTTP 200 with email:null (yields no values).
//
// VERIFIED from docs: base, X-API-Key auth, email-finder endpoint + inputs, response email/status/
// company_name/company_size/company_industry. (Mobile/personal-email endpoints need a LinkedIn
// profile_url → out of scope here.) Field names pinned UNVERIFIED until a live call (see hunter.go).
func LeadMagic(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.leadmagic.io/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "leadmagic",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-API-Key", KeyPoolSelector: "leadmagic:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.75},
			{Field: domain.FieldEmployeeCount, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 3, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			m := map[string]string{}
			putIf(m, "first_name", req.Known[domain.FieldFirstName])
			putIf(m, "last_name", req.Known[domain.FieldLastName])
			putIf(m, "full_name", req.Known[domain.FieldFullName])
			putIf(m, "domain", req.Known[domain.FieldCompanyDomain])
			putIf(m, "company_name", req.Known[domain.FieldCompanyName])
			b, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/people/email-finder", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email           string          `json:"email"`
				Status          string          `json:"status"`
				CompanyName     string          `json:"company_name"`
				CompanySize     json.RawMessage `json:"company_size"`
				CompanyIndustry string          `json:"company_industry"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, p.Email, 0.85)
			put(domain.FieldEmailStatus, p.Status, 0.75)
			put(domain.FieldCompanyName, p.CompanyName, 0.75)
			put(domain.FieldIndustry, p.CompanyIndustry, 0.65)
			put(domain.FieldEmployeeCount, rawStr(p.CompanySize), 0.65)
			return res, nil
		},
	}
}
