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

// Brandfetch builds an adapter for the Brandfetch Brand API (docs/03 §7) — brand + firmographic
// data by domain.
//   - Endpoint: GET {base}/{domain}  (base default https://api.brandfetch.io/v2/brands)
//     [docs.brandfetch.com/brand-api/overview].
//   - Auth: Bearer token, injected at egress (AuthBearer).
//   - Input: company_domain (path). Fills: company_name, employee_count, founded year, industry,
//     company type (`kind`), HQ city/country, and company LinkedIn (from links[]).
//   - No match returns 404 (NOT_FOUND) per the standard map.
//
// VERIFIED from docs (llms-full.txt schema): endpoint, Bearer auth, name + company{employees,
// foundedYear, industries[].name, kind, location{city,country}} + links[]{name,url}. Field names
// pinned UNVERIFIED until a live authorized call (see hunter.go).
func Brandfetch(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.brandfetch.io/v2/brands"
	}
	return &provider.HTTPAdapter{
		NameV:   "brandfetch",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "brandfetch:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyFoundedYear, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldIndustry, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyType, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyHQCountry, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyHQCity, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 3, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(req.Known[domain.FieldCompanyDomain])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: decodeBrandfetch,
	}
}

func decodeBrandfetch(body []byte) (provider.Result, error) {
	var p struct {
		Name    string `json:"name"`
		Company struct {
			Employees   int64  `json:"employees"`
			FoundedYear int64  `json:"foundedYear"`
			Kind        string `json:"kind"`
			Industries  []struct {
				Name string `json:"name"`
			} `json:"industries"`
			Location struct {
				City    string `json:"city"`
				Country string `json:"country"`
			} `json:"location"`
		} `json:"company"`
		Links []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"links"`
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
	put(domain.FieldCompanyType, p.Company.Kind, 0.70)
	put(domain.FieldCompanyHQCountry, p.Company.Location.Country, 0.70)
	put(domain.FieldCompanyHQCity, p.Company.Location.City, 0.70)
	if p.Company.Employees > 0 {
		put(domain.FieldEmployeeCount, itoa(p.Company.Employees), 0.65)
	}
	if p.Company.FoundedYear > 0 {
		put(domain.FieldCompanyFoundedYear, itoa(p.Company.FoundedYear), 0.70)
	}
	if len(p.Company.Industries) > 0 {
		put(domain.FieldIndustry, p.Company.Industries[0].Name, 0.65)
	}
	for _, l := range p.Links {
		if strings.EqualFold(l.Name, "linkedin") && l.URL != "" {
			put(domain.FieldCompanyLinkedInURL, l.URL, 0.75)
			break
		}
	}
	return res, nil
}
