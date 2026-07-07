package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Extruct builds an adapter for the Extruct v1 company-lookup API (docs/03 §7).
//   - Endpoint: GET {base}/v1/companies/{domain}  (base default https://api.extruct.ai)
//     [docs.extruct.ai/api-reference/company-lookup].
//   - Auth: Bearer token, injected at egress.
//   - Input: company_domain (path). Fills company_name/domain + employee_count. Company-only (no
//     person/email); no explicit industry field (only free-text descriptions) so industry not mapped.
//
// VERIFIED from docs: base, Bearer auth, GET /v1/companies/{id}, company_name/domain/context.
// employee_count. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Extruct(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.extruct.ai"
	}
	return &provider.HTTPAdapter{
		NameV:   "extruct",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "extruct:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 3, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := base + "/v1/companies/" + url.PathEscape(req.Known[domain.FieldCompanyDomain])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				CompanyName string `json:"company_name"`
				Domain      string `json:"domain"`
				Context     struct {
					EmployeeCount json.RawMessage `json:"employee_count"`
				} `json:"context"`
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
			put(domain.FieldCompanyName, p.CompanyName, 0.80)
			put(domain.FieldCompanyDomain, p.Domain, 0.85)
			put(domain.FieldEmployeeCount, rawStr(p.Context.EmployeeCount), 0.65)
			return res, nil
		},
	}
}
