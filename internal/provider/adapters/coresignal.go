package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Coresignal builds an adapter for the Coresignal Multi-source Company enrich API (docs/03 §7).
//   - Endpoint: GET {base}?website=  (base default
//     https://api.coresignal.com/cdapi/v2/company_multi_source/enrich) [docs.coresignal.com].
//   - Auth: 32-char API key in the "apikey" header (raw, no prefix), injected at egress
//     (AuthAPIKeyHeader).
//   - Input: company_domain. Fills firmographics + NAICS/SIC + company LinkedIn + funding stage.
//   - Status: DEPRIORITIZED (ADR-0009) — multi-source data is public-web / LinkedIn-derived; off by
//     default until a per-provider compliance review. 402 = insufficient credits (QUOTA).
//
// VERIFIED from docs: endpoint, apikey auth, response field names, status codes (402/404/409/422/
// 429, no 403). Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Coresignal(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.coresignal.com/cdapi/v2/company_multi_source/enrich"
	}
	return &provider.HTTPAdapter{
		NameV:   "coresignal",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "apikey",
			KeyPoolSelector: "coresignal:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 6, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyFoundedYear, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCountry, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCity, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldNAICS, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldSIC, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldFundingStage, Cost: 6, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("website", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				return nil, err
			}
			r.Header.Set("Accept", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				CompanyName    string   `json:"company_name"`
				WebsiteDomain  string   `json:"website_domain"`
				Industry       string   `json:"industry"`
				EmployeesCount int64    `json:"employees_count"`
				FoundedYear    string   `json:"founded_year"`
				HQCountry      string   `json:"hq_country"`
				HQCity         string   `json:"hq_city"`
				Type           string   `json:"type"`
				NetworkURL     string   `json:"professional_network_url"`
				NAICSCodes     []string `json:"naics_codes"`
				SICCodes       []string `json:"sic_codes"`
				LastFunding    struct {
					Type string `json:"type"`
				} `json:"last_funding_round"`
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
			put(domain.FieldCompanyName, p.CompanyName, 0.80)
			put(domain.FieldCompanyDomain, p.WebsiteDomain, 0.85)
			put(domain.FieldIndustry, p.Industry, 0.80)
			put(domain.FieldCompanyFoundedYear, p.FoundedYear, 0.80)
			put(domain.FieldCompanyHQCountry, p.HQCountry, 0.80)
			put(domain.FieldCompanyHQCity, p.HQCity, 0.80)
			put(domain.FieldCompanyType, p.Type, 0.80)
			put(domain.FieldCompanyLinkedInURL, p.NetworkURL, 0.80)
			put(domain.FieldNAICS, normList(p.NAICSCodes), 0.80)
			put(domain.FieldSIC, normList(p.SICCodes), 0.80)
			put(domain.FieldFundingStage, p.LastFunding.Type, 0.70)
			if p.EmployeesCount > 0 {
				put(domain.FieldEmployeeCount, itoa(p.EmployeesCount), 0.70)
			}
			return res, nil
		},
	}
}
