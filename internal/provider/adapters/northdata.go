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

// NorthData builds an adapter for the North Data company Data API (docs/03 §7) — official European
// business-register data (Handelsregister etc.) across ~19 countries.
//   - Endpoint: GET {base}/company/v1/company?name=&address=&fuzzyMatch=true&financials=true&extras=true
//     (base default https://www.northdata.com/_api — the swagger host is .de but every official
//     example uses .com) [github.com/northdata/api user guide].
//   - Auth: API key in the "X-Api-Key" HEADER, injected at egress (AuthAPIKeyHeader).
//   - Input: company_name (+company_hq_city as the address hint). Fills company_name (name.name),
//     company_type (name.legalForm — registry abbreviation like "AG"/"GmbH"), company_hq_city/country
//     (address.*), industry (segmentCodes.nace[0] — a NACE CODE, not a label), naics
//     (segmentCodes.naics[0] — crosswalked one-to-many, ~0.6), company_revenue + employee_count
//     (financials.items by indicator id; casing "Revenue"/"revenue" is UNVERIFIED so matched
//     case-insensitively), company_domain + company_phone (extras items url/phone — third-party
//     sourced, ~0.6). uksic is deliberately NOT mapped to sic (UK SIC 2007 ≠ US SIC).
//   - Status: DEPRIORITIZED (ADR-0009) — clean OpenAPI-documented registry data, but keys are issued
//     manually (email support) with a €500/month 12-month minimum: heavy onboarding, off by default.
//   - Quirk: 404 = not found (also fuzzyMatch below threshold); 503 = retry-advised transient.
//
// VERIFIED from the official user guide + swagger.yaml: endpoint, X-Api-Key header, response schema,
// segmentCodes example. Financial indicator id casing pinned UNVERIFIED until a live key (see hunter.go).
func NorthData(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://www.northdata.com/_api"
	}
	return &provider.HTTPAdapter{
		NameV:   "north-data",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-Api-Key",
			KeyPoolSelector: "north-data:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 2, ExpectedConfidence: 0.93},
			{Field: domain.FieldCompanyType, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCity, Cost: 2, ExpectedConfidence: 0.92},
			{Field: domain.FieldCompanyHQCountry, Cost: 2, ExpectedConfidence: 0.92},
			{Field: domain.FieldIndustry, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldNAICS, Cost: 2, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyRevenue, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 2, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyDomain, Cost: 2, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyPhone, Cost: 2, ExpectedConfidence: 0.55},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/company/v1/company")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("name", req.Known[domain.FieldCompanyName])
			setIfQ(q, "address", req.Known[domain.FieldCompanyHQCity])
			q.Set("fuzzyMatch", "true")
			q.Set("financials", "true")
			q.Set("extras", "true")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Name struct {
					Name      string `json:"name"`
					LegalForm string `json:"legalForm"`
				} `json:"name"`
				Address struct {
					City    string `json:"city"`
					Country string `json:"country"`
				} `json:"address"`
				SegmentCodes struct {
					NACE  []string `json:"nace"`
					NAICS []string `json:"naics"`
				} `json:"segmentCodes"`
				Financials struct {
					Items []struct {
						ID    string  `json:"id"`
						Value float64 `json:"value"`
					} `json:"items"`
				} `json:"financials"`
				Extras []struct {
					Items []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"items"`
				} `json:"extras"`
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
			put(domain.FieldCompanyName, p.Name.Name, 0.93)
			put(domain.FieldCompanyType, p.Name.LegalForm, 0.90)
			put(domain.FieldCompanyHQCity, p.Address.City, 0.92)
			put(domain.FieldCompanyHQCountry, p.Address.Country, 0.92)
			if len(p.SegmentCodes.NACE) > 0 {
				put(domain.FieldIndustry, p.SegmentCodes.NACE[0], 0.80)
			}
			if len(p.SegmentCodes.NAICS) > 0 {
				put(domain.FieldNAICS, p.SegmentCodes.NAICS[0], 0.60)
			}
			// Indicator id casing is UNVERIFIED ("Revenue" in the reference, "revenue" in swagger
			// prose) — match case-insensitively rather than guess.
			for _, it := range p.Financials.Items {
				switch strings.ToLower(it.ID) {
				case "revenue":
					put(domain.FieldCompanyRevenue, itoa(int64(it.Value)), 0.80)
				case "employees":
					put(domain.FieldEmployeeCount, itoa(int64(it.Value)), 0.75)
				}
			}
			for _, ex := range p.Extras {
				for _, it := range ex.Items {
					switch it.ID {
					case "url":
						put(domain.FieldCompanyDomain, bareDomain(it.Value), 0.60)
					case "phone":
						put(domain.FieldCompanyPhone, it.Value, 0.55)
					}
				}
			}
			return res, nil
		},
	}
}
