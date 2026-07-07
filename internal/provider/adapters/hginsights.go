package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// HGInsights builds an adapter for the HG Insights v2 Companies Enrich API (docs/03 §7) —
// install-base technographics + firmographics.
//   - Endpoint: POST {base} with JSON {companies:{domains:[..]}, fields:[..]}  (base default
//     https://api.hginsights.com/data-api/v2/companies/enrich) [data-docs.hginsights.com/v2].
//   - Auth: Bearer token (hg_v2_…), injected at egress (AuthBearer).
//   - Input: company_domain. Fills: technographics (comma-joined install-base product names) +
//     firmographics (name, domain, country, employees, revenue).
//
// VERIFIED from docs: endpoint, Bearer auth, request body, companies[].firmographics.* +
// technographics.installs[].product response shape. Per-install field keys pinned UNVERIFIED until
// a live authorized call (see hunter.go). NOTE: HG v1 sunsets 2026-09-01 — this targets v2.
func HGInsights(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.hginsights.com/data-api/v2/companies/enrich"
	}
	return &provider.HTTPAdapter{
		NameV:   "hg-insights",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "hg-insights:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.95},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]any{
				"companies": map[string]any{"domains": []string{req.Known[domain.FieldCompanyDomain]}},
				"fields":    []string{"firmographics", "technographics"},
			}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Companies []struct {
					Firmographics struct {
						Name           string `json:"name"`
						Domain         string `json:"domain"`
						CountryCode    string `json:"country_code"`
						EmployeesTotal int64  `json:"employees_total"`
						RevenueTotal   int64  `json:"revenue_total"`
					} `json:"firmographics"`
					Technographics struct {
						Installs []struct {
							Product string `json:"product"`
						} `json:"installs"`
					} `json:"technographics"`
				} `json:"companies"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Companies) == 0 {
				return res, nil
			}
			c := p.Companies[0]
			put := func(f domain.Field, v string, conf domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: conf}
				}
			}
			put(domain.FieldCompanyName, c.Firmographics.Name, 0.90)
			put(domain.FieldCompanyDomain, c.Firmographics.Domain, 0.95)
			put(domain.FieldCompanyHQCountry, c.Firmographics.CountryCode, 0.80)
			if c.Firmographics.EmployeesTotal > 0 {
				put(domain.FieldEmployeeCount, itoa(c.Firmographics.EmployeesTotal), 0.65)
			}
			if c.Firmographics.RevenueTotal > 0 {
				put(domain.FieldCompanyRevenue, itoa(c.Firmographics.RevenueTotal), 0.65)
			}
			techs := make([]string, 0, len(c.Technographics.Installs))
			for _, in := range c.Technographics.Installs {
				techs = append(techs, in.Product)
			}
			put(domain.FieldTechnographics, normList(techs), 0.70)
			return res, nil
		},
	}
}
