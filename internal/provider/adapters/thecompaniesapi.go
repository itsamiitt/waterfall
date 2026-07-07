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

// TheCompaniesAPI builds an adapter for The Companies API firmographics (docs/03 §7).
//   - Endpoint: GET {base}/{domain}  (base default https://api.thecompaniesapi.com/v2/companies)
//     [thecompaniesapi.com/api/enrich-company-from-domain].
//   - Auth: header "Authorization: Basic <token>" where <token> is the RAW token (NOT base64 Basic,
//     NOT Bearer). The key-pool secret must be stored WITH the literal "Basic " prefix, injected at
//     egress (AuthAPIKeyHeader on "Authorization").
//   - Input: company_domain (path). Fills firmographics + NAICS/SIC + technographics + LinkedIn.
//
// VERIFIED from docs: endpoint, "Authorization: Basic <token>" auth, about.*/finances.*/codes.*/
// technologies.active/socials.linkedin paths. Some nested subkeys + body wrapping are UNVERIFIED
// until a live authorized call (see hunter.go).
func TheCompaniesAPI(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.thecompaniesapi.com/v2/companies"
	}
	return &provider.HTTPAdapter{
		NameV:   "the-companies-api",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization",
			KeyPoolSelector: "the-companies-api:default", // secret stored as "Basic <token>"
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldNAICS, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.60},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(req.Known[domain.FieldCompanyDomain])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				About struct {
					Name                string `json:"name"`
					Industry            string `json:"industry"`
					BusinessType        string `json:"businessType"`
					YearFounded         int64  `json:"yearFounded"`
					TotalEmployees      string `json:"totalEmployees"`
					TotalEmployeesExact int64  `json:"totalEmployeesExact"`
				} `json:"about"`
				Finances struct {
					Revenue string `json:"revenue"`
				} `json:"finances"`
				Locations struct {
					Headquarters struct {
						City    string `json:"city"`
						Country string `json:"country"`
					} `json:"headquarters"`
				} `json:"locations"`
				Socials struct {
					LinkedIn struct {
						URL string `json:"url"`
					} `json:"linkedin"`
				} `json:"socials"`
				Codes struct {
					NAICS []string `json:"naics"`
					SIC   []string `json:"sic"`
				} `json:"codes"`
				Technologies struct {
					Active []string `json:"active"`
				} `json:"technologies"`
				Contacts struct {
					Phones []string `json:"phones"`
				} `json:"contacts"`
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
			put(domain.FieldCompanyName, p.About.Name, 0.85)
			put(domain.FieldIndustry, p.About.Industry, 0.80)
			put(domain.FieldCompanyType, p.About.BusinessType, 0.80)
			put(domain.FieldCompanyRevenue, p.Finances.Revenue, 0.60)
			put(domain.FieldCompanyHQCity, p.Locations.Headquarters.City, 0.75)
			put(domain.FieldCompanyHQCountry, p.Locations.Headquarters.Country, 0.75)
			put(domain.FieldCompanyLinkedInURL, p.Socials.LinkedIn.URL, 0.80)
			put(domain.FieldTechnographics, normList(p.Technologies.Active), 0.70)
			if p.About.YearFounded > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.About.YearFounded), 0.80)
			}
			switch {
			case p.About.TotalEmployeesExact > 0:
				put(domain.FieldEmployeeCount, itoa(p.About.TotalEmployeesExact), 0.65)
			default:
				put(domain.FieldEmployeeCount, p.About.TotalEmployees, 0.60)
			}
			if len(p.Codes.NAICS) > 0 {
				put(domain.FieldNAICS, p.Codes.NAICS[0], 0.85)
			}
			if len(p.Codes.SIC) > 0 {
				put(domain.FieldSIC, p.Codes.SIC[0], 0.85)
			}
			if len(p.Contacts.Phones) > 0 {
				put(domain.FieldCompanyPhone, p.Contacts.Phones[0], 0.60)
			}
			return res, nil
		},
	}
}
