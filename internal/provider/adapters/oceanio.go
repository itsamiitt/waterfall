package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// OceanIO builds an adapter for the Ocean.io company enrichment API (docs/03 §7).
//   - Endpoint: POST {base} with JSON {"company":{"domain":..,"name":..}}  (base default
//     https://api.ocean.io/v2/enrich/company) [app.ocean.io/docs/enrich/enrichCompany].
//   - Auth: API token in the "X-Api-Token" header, injected at egress (AuthAPIKeyHeader).
//   - Input: company_domain (+ optional company_name). Fills firmographics + funding signal +
//     technographics.
//   - Quirk: HTTP 201 "data gathering started" is a soft-miss (crawl triggered, no body, no charge)
//     — 2xx so it decodes to no values and the waterfall falls through (retry-later semantics noted).
//
// VERIFIED from docs: endpoint, X-Api-Token auth, request/response shape. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func OceanIO(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.ocean.io/v2/enrich/company"
	}
	return &provider.HTTPAdapter{
		NameV:   "ocean-io",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-Api-Token",
			KeyPoolSelector: "ocean-io:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldFundingStage, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			company := map[string]string{}
			putIf(company, "domain", req.Known[domain.FieldCompanyDomain])
			putIf(company, "name", req.Known[domain.FieldCompanyName])
			b, err := json.Marshal(map[string]any{"company": company})
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
				Domain       string   `json:"domain"`
				Name         string   `json:"name"`
				CompanySize  string   `json:"companySize"`
				PrimaryCntry string   `json:"primaryCountry"`
				Revenue      string   `json:"revenue"`
				YearFounded  int64    `json:"yearFounded"`
				Industries   []string `json:"industries"`
				Technologies []string `json:"technologies"`
				FundingRound struct {
					Type string `json:"type"`
				} `json:"fundingRound"`
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
			put(domain.FieldCompanyDomain, p.Domain, 0.85)
			put(domain.FieldCompanyName, p.Name, 0.85)
			put(domain.FieldEmployeeCount, p.CompanySize, 0.65)
			put(domain.FieldCompanyRevenue, p.Revenue, 0.60)
			put(domain.FieldCompanyHQCountry, p.PrimaryCntry, 0.80)
			put(domain.FieldIndustry, normList(p.Industries), 0.75)
			put(domain.FieldTechnographics, normList(p.Technologies), 0.70)
			put(domain.FieldFundingStage, p.FundingRound.Type, 0.70)
			if p.YearFounded > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.YearFounded), 0.80)
			}
			return res, nil
		},
	}
}
