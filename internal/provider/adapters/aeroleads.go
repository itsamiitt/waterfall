package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// AeroLeads builds an adapter for the AeroLeads email-finder API (docs/03 §7).
//   - Endpoint: GET {base}/apis/details?first_name=&last_name=&company_url=  (base default
//     https://aeroleads.com) [aeroleads.com/blog/how-to-use-aeroleads-api].
//   - Auth: api_key in the "api_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: first_name+last_name+company_domain (as company_url). Fills work_email + email_status
//     (confidence "1.0"/"0.5"/"0.1") + full_name + company_domain (echo).
//
// VERIFIED from docs: base, api_key query auth, /apis/details inputs, response {name,company_url,
// emails[]{email,status}}. Field names pinned UNVERIFIED until a live authorized call (hunter.go).
func AeroLeads(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://aeroleads.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "aeroleads",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyQuery, QueryParam: "api_key", KeyPoolSelector: "aeroleads:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.78},
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.72},
			{Field: domain.FieldFullName, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/apis/details")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "first_name", req.Known[domain.FieldFirstName])
			setIf(q, "last_name", req.Known[domain.FieldLastName])
			setIf(q, "company_url", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Name       string `json:"name"`
				CompanyURL string `json:"company_url"`
				Emails     []struct {
					Email  string `json:"email"`
					Status string `json:"status"`
				} `json:"emails"`
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
			put(domain.FieldFullName, p.Name, 0.80)
			put(domain.FieldCompanyDomain, bareDomain(p.CompanyURL), 0.70)
			if len(p.Emails) > 0 {
				put(domain.FieldWorkEmail, p.Emails[0].Email, 0.78)
				put(domain.FieldEmailStatus, p.Emails[0].Status, 0.72)
			}
			return res, nil
		},
	}
}
