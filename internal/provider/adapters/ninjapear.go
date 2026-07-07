package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// NinjaPear builds an adapter for the NinjaPear (Nubela) Company Details API (docs/03 §7).
//   - Endpoint: GET {base}?website=&include_employee_count=true  (base default
//     https://nubela.co/api/v1/company/details) [nubela.co/docs].
//   - Auth: Bearer token, injected at egress (AuthBearer).
//   - Input: company_domain (website). Fills firmographics (name, domain, industry[GICS code],
//     type, founded year, employees, HQ country/city). No match = 404.
//   - Status: DEPRIORITIZED (ADR-0009) — public-web aggregation (modeled firmo, not a registry).
//   - Quirk: 403 = out of credits (QUOTA), not rate-limit; the shared map treats 403 as RATE_LIMIT.
//
// VERIFIED from docs: endpoint, Bearer auth, flat response fields (name/websites[]/industry/
// company_type/founded_year/employee_count/addresses[]). `industry` is an 8-digit GICS numeric code,
// not text. No revenue/naics/sic/DUNS/LinkedIn returned. Field names UNVERIFIED (see hunter.go).
func NinjaPear(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://nubela.co/api/v1/company/details"
	}
	return &provider.HTTPAdapter{
		NameV:   "ninjapear",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "ninjapear:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.60},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "website", req.Known[domain.FieldCompanyDomain])
			q.Set("include_employee_count", "true")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Name          string   `json:"name"`
				Websites      []string `json:"websites"`
				Industry      int64    `json:"industry"`
				CompanyType   string   `json:"company_type"`
				FoundedYear   int64    `json:"founded_year"`
				EmployeeCount int64    `json:"employee_count"`
				Addresses     []struct {
					AddressType string `json:"address_type"`
					City        string `json:"city"`
					Country     string `json:"country"`
				} `json:"addresses"`
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
			put(domain.FieldCompanyName, p.Name, 0.65)
			put(domain.FieldCompanyType, p.CompanyType, 0.65)
			if len(p.Websites) > 0 {
				put(domain.FieldCompanyDomain, bareDomain(p.Websites[0]), 0.70)
			}
			if p.Industry > 0 {
				put(domain.FieldIndustry, itoa(p.Industry), 0.60)
			}
			if p.FoundedYear > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.FoundedYear), 0.60)
			}
			if p.EmployeeCount > 0 {
				put(domain.FieldEmployeeCount, itoa(p.EmployeeCount), 0.60)
			}
			for _, a := range p.Addresses {
				if a.AddressType == "HEADQUARTERS" {
					put(domain.FieldCompanyHQCountry, a.Country, 0.65)
					put(domain.FieldCompanyHQCity, a.City, 0.60)
				}
			}
			return res, nil
		},
	}
}
