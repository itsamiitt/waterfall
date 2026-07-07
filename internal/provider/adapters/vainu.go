package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Vainu builds an adapter for the Vainu v2 Companies API (docs/03 §7) — registry-backed Nordics/EU
// firmographics.
//   - Endpoint: GET {base}?domain__in=&company_name=&country=&fields=  (base default
//     https://api.vainu.io/api/v2/companies/) [developers.vainu.com/reference/listcompanies].
//   - Auth: raw API key in the "API-Key" header, injected at egress (AuthAPIKeyHeader).
//   - Input: company_domain / company_name / company_hq_country. Fills registry firmographics +
//     technographics. It is a filter/search endpoint: no match = 200 with empty results[].
//   - The adapter sets an explicit `fields` list so the mapped fields are guaranteed to return.
//
// VERIFIED from docs + a live 401 probe: endpoint, API-Key auth, {results:[…]} envelope, field
// names. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Vainu(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.vainu.io/api/v2/companies/"
	}
	return &provider.HTTPAdapter{
		NameV:   "vainu",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "API-Key",
			KeyPoolSelector: "vainu:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "domain__in", req.Known[domain.FieldCompanyDomain])
			setIf(q, "company_name", req.Known[domain.FieldCompanyName])
			setIf(q, "country", req.Known[domain.FieldCompanyHQCountry])
			q.Set("fields", "company_name,domain,country,city,staff_number,turn_over,industry_code,form_of_company,foundation_date,linkedin_link,phone,technologies")
			q.Set("limit", "1")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Results []struct {
					CompanyName    string   `json:"company_name"`
					Domain         string   `json:"domain"`
					Country        string   `json:"country"`
					City           string   `json:"city"`
					StaffNumber    int64    `json:"staff_number"`
					TurnOver       string   `json:"turn_over"`
					IndustryCode   string   `json:"industry_code"`
					FormOfCompany  string   `json:"form_of_company"`
					FoundationDate string   `json:"foundation_date"`
					LinkedInLink   string   `json:"linkedin_link"`
					Phone          string   `json:"phone"`
					Technologies   []string `json:"technologies"`
				} `json:"results"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Results) == 0 {
				return res, nil
			}
			r := p.Results[0]
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, r.CompanyName, 0.80)
			put(domain.FieldCompanyDomain, r.Domain, 0.80)
			put(domain.FieldCompanyHQCountry, r.Country, 0.80)
			put(domain.FieldCompanyHQCity, r.City, 0.75)
			put(domain.FieldCompanyRevenue, r.TurnOver, 0.70)
			put(domain.FieldIndustry, r.IndustryCode, 0.75)
			put(domain.FieldCompanyType, r.FormOfCompany, 0.80)
			put(domain.FieldCompanyFoundedYear, yearOf(r.FoundationDate), 0.80)
			put(domain.FieldCompanyLinkedInURL, r.LinkedInLink, 0.75)
			put(domain.FieldCompanyPhone, r.Phone, 0.70)
			put(domain.FieldTechnographics, normList(r.Technologies), 0.65)
			if r.StaffNumber > 0 {
				put(domain.FieldEmployeeCount, itoa(r.StaffNumber), 0.70)
			}
			return res, nil
		},
	}
}
