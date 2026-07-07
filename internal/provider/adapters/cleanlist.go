package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Cleanlist builds an adapter for the Cleanlist.ai v2 COMPANY enrichment endpoint (docs/03 §7).
// Only the company endpoint is modeled: it is a clean SYNCHRONOUS single-shot lookup by domain/name.
// The person/bulk endpoints are async AND require a stateful lead_list_id + signed quote_id (bound
// to a pre-created lead list), which the stateless adapter model can't supply — deferred (see §6).
//   - Endpoint: POST {base}/enrichment/company with {"domain":…,"company_name":…}  (base default
//     https://api.cleanlist.ai/api/v2) [docs.cleanlist.ai/mcp-api/enrichment].
//   - Auth: Bearer token (keys prefixed "clapi_"), injected at egress.
//   - Fills firmographics from the $.company object.
//
// VERIFIED from docs: v2 base, Bearer auth, POST /enrichment/company, $.company.{name,domain,
// industry,linkedin_url,revenue_range,employee_count}. Field names/types pinned UNVERIFIED until a
// live authorized call (see hunter.go); employee_count read via rawStr (int-or-string uncertain).
func Cleanlist(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.cleanlist.ai/api/v2"
	}
	return &provider.HTTPAdapter{
		NameV:   "cleanlist",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "cleanlist:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 4, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyRevenue, Cost: 4, ExpectedConfidence: 0.65},
			{Field: domain.FieldEmployeeCount, Cost: 4, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 4, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]string{}
			putIf(body, "domain", req.Known[domain.FieldCompanyDomain])
			putIf(body, "company_name", req.Known[domain.FieldCompanyName])
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/enrichment/company", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Company struct {
					Name          string          `json:"name"`
					Domain        string          `json:"domain"`
					Industry      string          `json:"industry"`
					LinkedInURL   string          `json:"linkedin_url"`
					RevenueRange  string          `json:"revenue_range"`
					EmployeeCount json.RawMessage `json:"employee_count"`
				} `json:"company"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			c := p.Company
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, conf domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: conf}
				}
			}
			put(domain.FieldCompanyName, c.Name, 0.80)
			put(domain.FieldCompanyDomain, c.Domain, 0.85)
			put(domain.FieldIndustry, c.Industry, 0.75)
			put(domain.FieldCompanyLinkedInURL, c.LinkedInURL, 0.75)
			put(domain.FieldCompanyRevenue, c.RevenueRange, 0.65)
			put(domain.FieldEmployeeCount, rawStr(c.EmployeeCount), 0.70)
			return res, nil
		},
	}
}
