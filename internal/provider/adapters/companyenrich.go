package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// CompanyEnrich builds an adapter for the CompanyEnrich firmographics API (docs/03 §7).
//   - Endpoint: GET {base}?domain=  (base default https://api.companyenrich.com/companies/enrich)
//     [docs.companyenrich.com/reference/get_companies-enrich].
//   - Auth: static bearer token "Authorization: Bearer <token>", injected at egress (AuthBearer).
//   - Input: company_domain. Fills company_name/domain/type/industry, employee_count + company_revenue
//     (modeled BUCKET enums like "5K-10K"/"over-1b", ~0.65), funding_stage, company_founded_year,
//     company_hq_country/city, company_linkedin_url, company_phone, naics (array→normalized),
//     technographics (array→normalized).
//   - Quirk: 404 = domain could not be enriched = the no-match signal (classifyStatus→ClassNotFound);
//     402 = insufficient credits (QUOTA); 429 = 300 req/min. The CompanyInfo object is returned at
//     the top level (no envelope).
//
// VERIFIED from docs: endpoint, bearer auth, bucketed employees/revenue, nested location/financial/
// socials, 404 no-match. Exact JSON field names pinned UNVERIFIED until a live key (see hunter.go).
func CompanyEnrich(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.companyenrich.com/companies/enrich"
	}
	return &provider.HTTPAdapter{
		NameV:   "companyenrich",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "companyenrich:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyType, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 1, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyRevenue, Cost: 1, ExpectedConfidence: 0.65},
			{Field: domain.FieldFundingStage, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyFoundedYear, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCountry, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyPhone, Cost: 1, ExpectedConfidence: 0.65},
			{Field: domain.FieldNAICS, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldTechnographics, Cost: 1, ExpectedConfidence: 0.65},
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
				Name         string   `json:"name"`
				Domain       string   `json:"domain"`
				Type         string   `json:"type"`
				Industry     string   `json:"industry"`
				Employees    string   `json:"employees"`
				Revenue      string   `json:"revenue"`
				FoundedYear  int64    `json:"founded_year"`
				NAICSCodes   []string `json:"naics_codes"`
				Technologies []string `json:"technologies"`
				Location     struct {
					Country struct {
						Name string `json:"name"`
					} `json:"country"`
					City struct {
						Name string `json:"name"`
					} `json:"city"`
					Phone string `json:"phone"`
				} `json:"location"`
				Financial struct {
					FundingStage string `json:"funding_stage"`
				} `json:"financial"`
				Socials struct {
					LinkedInURL string `json:"linkedin_url"`
				} `json:"socials"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, conf float64) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: domain.Confidence(conf).Clamp()}
				}
			}
			put(domain.FieldCompanyName, p.Name, 0.85)
			put(domain.FieldCompanyDomain, p.Domain, 0.85)
			put(domain.FieldCompanyType, p.Type, 0.85)
			put(domain.FieldIndustry, p.Industry, 0.85)
			put(domain.FieldEmployeeCount, p.Employees, 0.65)
			put(domain.FieldCompanyRevenue, p.Revenue, 0.65)
			put(domain.FieldFundingStage, p.Financial.FundingStage, 0.85)
			if p.FoundedYear > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.FoundedYear), 0.85)
			}
			put(domain.FieldCompanyHQCountry, p.Location.Country.Name, 0.85)
			put(domain.FieldCompanyHQCity, p.Location.City.Name, 0.85)
			put(domain.FieldCompanyLinkedInURL, p.Socials.LinkedInURL, 0.85)
			put(domain.FieldCompanyPhone, p.Location.Phone, 0.65)
			put(domain.FieldNAICS, normList(p.NAICSCodes), 0.85)
			put(domain.FieldTechnographics, normList(p.Technologies), 0.65)
			return res, nil
		},
	}
}
