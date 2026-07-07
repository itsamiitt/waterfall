package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Explorium builds an async match→fetch adapter for the Explorium firmographics API (docs/03 §7).
//   - Match: POST {base}/v1/businesses/match with {"businesses_to_match":[{name,domain,linkedin_url}]}
//     → matched_businesses[0].business_id (no id / request_status miss ⇒ NOT_FOUND).
//   - Fetch: POST {base}/v1/businesses/firmographics/enrich with {"business_id":<id>} → data.*
//     (returns synchronously — done on first fetch).
//   - Auth: API key in the "api_key" header, injected at egress.
//   - base default https://api.explorium.ai [developers.explorium.ai].
//
// VERIFIED from docs + OpenAPI: endpoints, api_key header, business_id token in the fetch BODY,
// data.* firmographics. employee_count/company_revenue are min/max range objects (rendered "min-max").
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Explorium(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.explorium.ai"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "explorium",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "api_key", KeyPoolSelector: "explorium:default"},
		Policy:       provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		PollInterval: time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.92},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldIndustry, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldNAICS, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldSIC, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldEmployeeCount, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyRevenue, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCountry, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCity, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 3, ExpectedConfidence: 0.88},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			m := map[string]string{}
			putIf(m, "name", req.Known[domain.FieldCompanyName])
			putIf(m, "domain", req.Known[domain.FieldCompanyDomain])
			putIf(m, "linkedin_url", req.Known[domain.FieldCompanyLinkedInURL])
			b, err := json.Marshal(map[string]any{"businesses_to_match": []map[string]string{m}})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/businesses/match", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				MatchedBusinesses []struct {
					BusinessID string `json:"business_id"`
				} `json:"matched_businesses"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if len(p.MatchedBusinesses) == 0 || p.MatchedBusinesses[0].BusinessID == "" {
				return "", domain.NewProviderError("explorium", domain.ClassNotFound, errResultsGone)
			}
			return p.MatchedBusinesses[0].BusinessID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			b, err := json.Marshal(map[string]string{"business_id": token})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/businesses/firmographics/enrich", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Data struct {
					Name                     string `json:"name"`
					Website                  string `json:"website"`
					LinkedInIndustryCategory string `json:"linkedin_industry_category"`
					NAICS                    string `json:"naics"`
					SICCode                  string `json:"sic_code"`
					CountryName              string `json:"country_name"`
					CityName                 string `json:"city_name"`
					LinkedInProfile          string `json:"linkedin_profile"`
					EmployeesRange           *struct {
						Min int64 `json:"min"`
						Max int64 `json:"max"`
					} `json:"number_of_employees_range"`
					RevenueRange *struct {
						Min int64 `json:"min"`
						Max int64 `json:"max"`
					} `json:"yearly_revenue_range"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			d := p.Data
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, d.Name, 0.92)
			put(domain.FieldCompanyDomain, bareDomain(d.Website), 0.80)
			put(domain.FieldIndustry, d.LinkedInIndustryCategory, 0.85)
			put(domain.FieldNAICS, d.NAICS, 0.90)
			put(domain.FieldSIC, d.SICCode, 0.90)
			put(domain.FieldCompanyHQCountry, d.CountryName, 0.90)
			put(domain.FieldCompanyHQCity, d.CityName, 0.90)
			put(domain.FieldCompanyLinkedInURL, d.LinkedInProfile, 0.88)
			if d.EmployeesRange != nil && d.EmployeesRange.Max > 0 {
				put(domain.FieldEmployeeCount, itoa(d.EmployeesRange.Min)+"-"+itoa(d.EmployeesRange.Max), 0.85)
			}
			if d.RevenueRange != nil && d.RevenueRange.Max > 0 {
				put(domain.FieldCompanyRevenue, itoa(d.RevenueRange.Min)+"-"+itoa(d.RevenueRange.Max), 0.85)
			}
			return res, true, nil
		},
	}
}
