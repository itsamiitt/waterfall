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

// AbstractCompany builds an adapter for the AbstractAPI Company Enrichment API (docs/03 §7).
//   - Endpoint: GET {base}?domain=  (base default https://companyenrichment.abstractapi.com/v2/)
//     [abstractapi.com/api/company-enrichment].
//   - Auth: api_key in the "api_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: company_domain. Fills firmographics. Unmatched domains return sparse JSON (missing keys).
//
// VERIFIED from docs + the MS connector schema: endpoint, api_key query, response keys name/domain/
// employees_count/industry/year_founded/country/type/linkedin_url. linkedin_url is returned without a
// scheme (normalized to https). Field names pinned UNVERIFIED until a live authorized call (hunter.go).
func AbstractCompany(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://companyenrichment.abstractapi.com/v2/"
	}
	return &provider.HTTPAdapter{
		NameV:   "abstract-company",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyQuery, QueryParam: "api_key", KeyPoolSelector: "abstract-company:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldIndustry, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyFoundedYear, Cost: 3, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyHQCountry, Cost: 3, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyType, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 3, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("domain", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Name           string `json:"name"`
				Domain         string `json:"domain"`
				EmployeesCount int64  `json:"employees_count"`
				Industry       string `json:"industry"`
				YearFounded    int64  `json:"year_founded"`
				Country        string `json:"country"`
				Type           string `json:"type"`
				LinkedInURL    string `json:"linkedin_url"`
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
			put(domain.FieldCompanyName, p.Name, 0.80)
			put(domain.FieldCompanyDomain, p.Domain, 0.85)
			put(domain.FieldIndustry, p.Industry, 0.70)
			put(domain.FieldCompanyHQCountry, p.Country, 0.75)
			put(domain.FieldCompanyType, p.Type, 0.65)
			if p.EmployeesCount > 0 {
				put(domain.FieldEmployeeCount, itoa(p.EmployeesCount), 0.70)
			}
			if p.YearFounded > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.YearFounded), 0.75)
			}
			if p.LinkedInURL != "" {
				li := p.LinkedInURL
				if !strings.HasPrefix(li, "http") {
					li = "https://" + li
				}
				put(domain.FieldCompanyLinkedInURL, li, 0.70)
			}
			return res, nil
		},
	}
}
