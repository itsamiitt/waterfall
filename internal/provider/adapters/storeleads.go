package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Storeleads builds an adapter for the Store Leads API (docs/03 §7) — e-commerce firmographics +
// technographics.
//   - Endpoint: GET {base}/{domain}  (base default https://storeleads.app/json/api/v1/all/domain)
//     [storeleads.app/api].
//   - Auth: Bearer token, injected at egress (AuthBearer).
//   - Input: company_domain. Fills firmographics + technographics (platform + apps + technologies).
//     Response is wrapped under a top-level "domain" object.
//   - Note: estimated_sales_yearly is annual sales in CENTS of USD — the adapter divides by 100 to
//     store company_revenue as a whole-dollar figure.
//
// VERIFIED from docs: endpoint, Bearer auth, top-level "domain" wrapper, field names (merchant_name,
// country_code, city, employee_count, estimated_sales_yearly[cents], platform, apps[].name,
// technologies[].name, categories). Field names pinned UNVERIFIED until a live authorized call.
func Storeleads(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://storeleads.app/json/api/v1/all/domain"
	}
	return &provider.HTTPAdapter{
		NameV:   "storeleads",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "storeleads:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.55},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(req.Known[domain.FieldCompanyDomain])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Domain struct {
					Name          string   `json:"name"`
					MerchantName  string   `json:"merchant_name"`
					Platform      string   `json:"platform"`
					CountryCode   string   `json:"country_code"`
					City          string   `json:"city"`
					EmployeeCount int64    `json:"employee_count"`
					SalesYearly   int64    `json:"estimated_sales_yearly"`
					Categories    []string `json:"categories"`
					Apps          []struct {
						Name string `json:"name"`
					} `json:"apps"`
					Technologies []struct {
						Name string `json:"name"`
					} `json:"technologies"`
				} `json:"domain"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			d := p.Domain
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyDomain, d.Name, 0.85)
			put(domain.FieldCompanyName, d.MerchantName, 0.80)
			put(domain.FieldCompanyHQCountry, d.CountryCode, 0.80)
			put(domain.FieldCompanyHQCity, d.City, 0.80)
			put(domain.FieldIndustry, normList(d.Categories), 0.55)
			if d.EmployeeCount > 0 {
				put(domain.FieldEmployeeCount, itoa(d.EmployeeCount), 0.65)
			}
			if d.SalesYearly > 0 {
				put(domain.FieldCompanyRevenue, itoa(d.SalesYearly/100), 0.60) // cents -> dollars
			}
			tech := make([]string, 0, len(d.Apps)+len(d.Technologies)+1)
			if d.Platform != "" {
				tech = append(tech, d.Platform)
			}
			for _, a := range d.Apps {
				tech = append(tech, a.Name)
			}
			for _, t := range d.Technologies {
				tech = append(tech, t.Name)
			}
			put(domain.FieldTechnographics, normList(tech), 0.75)
			return res, nil
		},
	}
}
