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

// Owler builds an adapter for the Owler Company Premium API (docs/03 §7) — crowdsourced competitive
// firmographics.
//   - Endpoint: GET {base}/{website}  (base default https://api.owler.com/v1/companypremium/url)
//     [developers.owler.com; OpenAPI mirror].
//   - Auth: API key in the "user_key" header, injected at egress (AuthAPIKeyHeader).
//   - Input: company_domain (path). Fills firmographics (name, industry, employees[str], revenue[str],
//     founded, type, HQ country/city/phone, LinkedIn). buying_signal (news feed) lives on a separate
//     /v1/feed/url endpoint and is intentionally not mapped here.
//   - Status: DEPRIORITIZED (ADR-0009) — crowdsourced/public-web provenance (Meltwater-owned).
//
// VERIFIED from the OpenAPI mirror: host, user_key auth, path, company field names. Field names
// pinned UNVERIFIED until a live authorized call (see hunter.go).
func Owler(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.owler.com/v1/companypremium/url"
	}
	return &provider.HTTPAdapter{
		NameV:   "owler",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "user_key",
			KeyPoolSelector: "owler:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(req.Known[domain.FieldCompanyDomain])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Name          string   `json:"name"`
				Website       string   `json:"website"`
				CompanyType   string   `json:"company_type"`
				EmployeeCount string   `json:"employee_count"`
				Revenue       string   `json:"revenue"`
				FoundedDate   string   `json:"founded_date"`
				Industries    []string `json:"industries"`
				LinkedInLink  string   `json:"linkedin_link"`
				HQAddress     struct {
					City    string `json:"city"`
					Country string `json:"country"`
					Phone   string `json:"phone"`
				} `json:"hq_address"`
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
			put(domain.FieldCompanyName, p.Name, 0.70)
			put(domain.FieldCompanyDomain, p.Website, 0.70)
			put(domain.FieldCompanyType, p.CompanyType, 0.65)
			put(domain.FieldEmployeeCount, p.EmployeeCount, 0.60)
			put(domain.FieldCompanyRevenue, p.Revenue, 0.60)
			put(domain.FieldCompanyFoundedYear, yearOf(p.FoundedDate), 0.60)
			put(domain.FieldCompanyLinkedInURL, p.LinkedInLink, 0.65)
			put(domain.FieldCompanyHQCountry, p.HQAddress.Country, 0.65)
			put(domain.FieldCompanyHQCity, p.HQAddress.City, 0.60)
			put(domain.FieldCompanyPhone, p.HQAddress.Phone, 0.60)
			if len(p.Industries) > 0 {
				put(domain.FieldIndustry, p.Industries[0], 0.65)
			}
			return res, nil
		},
	}
}
