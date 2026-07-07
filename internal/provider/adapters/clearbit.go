package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Clearbit builds an adapter for the Clearbit (HubSpot Breeze) Company Enrichment API (docs/03 §7).
//   - Endpoint: GET {base}?domain=  (base default https://company.clearbit.com/v2/companies/find)
//     [dashboard.clearbit.com/docs].
//   - Auth: Bearer token (sk_...), injected at egress (AuthBearer).
//   - Input: company_domain. Fills firmographics: name, industry, sic/naics, employees, revenue,
//     tech stack, HQ country/city, founded year, type, company LinkedIn.
//   - Quirk: 202 = lookup queued/not-yet-cached (retry later). classifyStatus treats 2xx as success,
//     so a pending 202 decodes to no fields and the waterfall falls through rather than retrying —
//     acceptable (no fabricated data); noted UNVERIFIED. 404 = no match (success-with-no-value).
//   - PRODUCT NOTE: the standalone API is on legacy/sunset footing post-HubSpot; net-new access is
//     Breeze Intelligence inside HubSpot (docs/03 §7 confidence_note).
//
// VERIFIED from docs: endpoint, Bearer auth, documented Company payload paths, 202 semantics. Field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Clearbit(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://company.clearbit.com/v2/companies/find"
	}
	return &provider.HTTPAdapter{
		NameV:   "clearbit",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "clearbit:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldNAICS, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.75},
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
		Decode: decodeClearbit,
	}
}

func decodeClearbit(body []byte) (provider.Result, error) {
	var p struct {
		Name     string `json:"name"`
		Category struct {
			Industry  string `json:"industry"`
			SICCode   string `json:"sicCode"`
			NAICSCode string `json:"naicsCode"`
		} `json:"category"`
		Metrics struct {
			Employees              int64  `json:"employees"`
			EstimatedAnnualRevenue string `json:"estimatedAnnualRevenue"`
		} `json:"metrics"`
		Tech []string `json:"tech"`
		Geo  struct {
			Country string `json:"country"`
			City    string `json:"city"`
		} `json:"geo"`
		FoundedYear int64  `json:"foundedYear"`
		Type        string `json:"type"`
		LinkedIn    struct {
			Handle string `json:"handle"`
		} `json:"linkedin"`
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
	put(domain.FieldCompanyName, p.Name, 0.85)
	put(domain.FieldIndustry, p.Category.Industry, 0.80)
	put(domain.FieldSIC, p.Category.SICCode, 0.65)
	put(domain.FieldNAICS, p.Category.NAICSCode, 0.65)
	put(domain.FieldCompanyRevenue, p.Metrics.EstimatedAnnualRevenue, 0.75)
	put(domain.FieldCompanyHQCountry, p.Geo.Country, 0.85)
	put(domain.FieldCompanyHQCity, p.Geo.City, 0.85)
	put(domain.FieldCompanyType, p.Type, 0.80)
	if p.Metrics.Employees > 0 {
		put(domain.FieldEmployeeCount, strconv.FormatInt(p.Metrics.Employees, 10), 0.80)
	}
	if p.FoundedYear > 0 {
		put(domain.FieldCompanyFoundedYear, strconv.FormatInt(p.FoundedYear, 10), 0.80)
	}
	if v := normList(p.Tech); v != "" {
		put(domain.FieldTechnographics, v, 0.75)
	}
	if h := strings.TrimSpace(p.LinkedIn.Handle); h != "" {
		put(domain.FieldCompanyLinkedInURL, "https://www.linkedin.com/"+strings.TrimLeft(h, "/"), 0.75)
	}
	return res, nil
}
