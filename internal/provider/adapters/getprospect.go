package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// GetProspect builds an adapter for the GetProspect v2 Email Finder (docs/03 §7).
//   - Endpoint: GET {base}/v2/email-finder?full_name=&first_name=&last_name=&domain=&company=
//     (base default https://api.getprospect.com) [getprospect.readme.io].
//   - Auth: API key in the "apiKey" header, injected at egress.
//   - Input: full_name / first+last AND company_domain / company_name. Fills work_email + status.
//
// VERIFIED from docs: base, apiKey header, v2 email-finder endpoint + inputs, response email/status/
// domain. Names/company are input-only (not echoed). Field names pinned UNVERIFIED (see hunter.go).
func GetProspect(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.getprospect.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "getprospect",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "apiKey", KeyPoolSelector: "getprospect:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.82},
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.78},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/v2/email-finder")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "full_name", req.Known[domain.FieldFullName])
			setIf(q, "first_name", req.Known[domain.FieldFirstName])
			setIf(q, "last_name", req.Known[domain.FieldLastName])
			setIf(q, "domain", req.Known[domain.FieldCompanyDomain])
			setIf(q, "company", req.Known[domain.FieldCompanyName])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email  string `json:"email"`
				Status string `json:"status"`
				Domain string `json:"domain"`
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
			put(domain.FieldWorkEmail, p.Email, 0.82)
			put(domain.FieldEmailStatus, p.Status, 0.78)
			put(domain.FieldCompanyDomain, p.Domain, 0.70)
			return res, nil
		},
	}
}
