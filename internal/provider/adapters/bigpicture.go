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

// BigPicture builds an adapter for the BigPicture.io Company API (docs/03 §7).
//   - Endpoint: GET {base}/v1/companies/find?domain=  (base default https://company.bigpicture.io)
//     [docs.bigpicture.io/api].
//   - Auth: the RAW API key is the entire Authorization header value — NO "Bearer" prefix — so this
//     is AuthAPIKeyHeader with HeaderName "Authorization" (docs curl: -H 'Authorization: {API_KEY}').
//   - Input: company_domain. Fills company_name/domain/type/phone, industry + naics (category.*),
//     company_founded_year, employee_count + company_revenue (metrics.*, modeled ~0.6),
//     company_hq_country/city (geo.*), company_linkedin_url (linkedin.handle is a BARE handle like
//     "company/uber-com" — Decode prefixes https://www.linkedin.com/).
//   - Quirk: 202 = lookup queued (domain not yet cached; no job token — re-request later). The shared
//     map treats 2xx as success; a 202 body decodes to no fields → NOT_FOUND semantics, and a later
//     retry can hit the cached 200. 404 = no matching company.
//
// VERIFIED from docs: endpoint, raw-key Authorization header, response field paths (Uber example),
// 202/404 behavior. Exact field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func BigPicture(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://company.bigpicture.io"
	}
	return &provider.HTTPAdapter{
		NameV:   "bigpicture",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization",
			KeyPoolSelector: "bigpicture:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyType, Cost: 1, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyPhone, Cost: 1, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 1, ExpectedConfidence: 0.65},
			{Field: domain.FieldNAICS, Cost: 1, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyFoundedYear, Cost: 1, ExpectedConfidence: 0.70},
			{Field: domain.FieldEmployeeCount, Cost: 1, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyRevenue, Cost: 1, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyHQCountry, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 1, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 1, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/v1/companies/find")
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
				Name        string `json:"name"`
				Domain      string `json:"domain"`
				Type        string `json:"type"`
				Phone       string `json:"phone"`
				FoundedYear int64  `json:"foundedYear"`
				Geo         struct {
					City    string `json:"city"`
					Country string `json:"country"`
				} `json:"geo"`
				Category struct {
					Industry  string `json:"industry"`
					NAICSCode string `json:"naicsCode"`
				} `json:"category"`
				Metrics struct {
					Employees     int64 `json:"employees"`
					AnnualRevenue int64 `json:"annualRevenue"`
				} `json:"metrics"`
				LinkedIn struct {
					Handle string `json:"handle"`
				} `json:"linkedin"`
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
			put(domain.FieldCompanyName, p.Name, 0.90)
			put(domain.FieldCompanyDomain, p.Domain, 0.90)
			put(domain.FieldCompanyType, p.Type, 0.80)
			put(domain.FieldCompanyPhone, p.Phone, 0.65)
			put(domain.FieldIndustry, p.Category.Industry, 0.65)
			put(domain.FieldNAICS, p.Category.NAICSCode, 0.60)
			if p.FoundedYear > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.FoundedYear), 0.70)
			}
			if p.Metrics.Employees > 0 {
				put(domain.FieldEmployeeCount, itoa(p.Metrics.Employees), 0.60)
			}
			if p.Metrics.AnnualRevenue > 0 {
				put(domain.FieldCompanyRevenue, itoa(p.Metrics.AnnualRevenue), 0.60)
			}
			put(domain.FieldCompanyHQCountry, p.Geo.Country, 0.85)
			put(domain.FieldCompanyHQCity, p.Geo.City, 0.80)
			if h := p.LinkedIn.Handle; h != "" {
				put(domain.FieldCompanyLinkedInURL, "https://www.linkedin.com/"+strings.TrimPrefix(h, "/"), 0.80)
			}
			return res, nil
		},
	}
}
