package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// CompaniesHouse builds a match→fetch adapter for the UK Companies House Public Data API (docs/03 §7).
//   - Match: GET {base}/search/companies?q={company_name} → items[0].company_number.
//   - Fetch: GET {base}/company/{company_number} → the company profile (terminal, single fetch).
//   - Auth: HTTP Basic with the API key as the username and an EMPTY password, injected at egress
//     (AuthBasic). The "<slug>:default" key pool must hold the full Basic credential "<API_KEY>:"
//     (trailing colon; egress base64-encodes the pool secret verbatim).
//   - base default https://api.company-information.service.gov.uk [developer.company-information.service.gov.uk].
//   - Fills company_name, company_type (type), company_founded_year (year of date_of_creation),
//     sic (sic_codes[]→normalized; UK SIC 2007, NOT NAICS), company_hq_country/city
//     (registered_office_address.{country,locality}). Official, free, GB-only registry.
//   - Quirk: search returns HTTP 200 with empty items[] on no match (not 404); ParseSubmit maps that
//     to ClassNotFound. 429 = >600 req / 5-min window (with Retry-After).
//
// VERIFIED from docs: base, Basic auth (key as username), search→profile paths, profile fields, the
// singular /company/{n} path. Exact field names pinned UNVERIFIED until a live key (see hunter.go).
func CompaniesHouse(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.company-information.service.gov.uk"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "companies-house",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBasic, KeyPoolSelector: "companies-house:default"},
		Policy:       provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		PollInterval: 1 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyType, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyFoundedYear, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldSIC, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCountry, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 1, ExpectedConfidence: 0.85},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/search/companies")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("q", req.Known[domain.FieldCompanyName])
			q.Set("items_per_page", "1")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Items []struct {
					CompanyNumber string `json:"company_number"`
				} `json:"items"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if len(p.Items) == 0 || p.Items[0].CompanyNumber == "" {
				return "", domain.NewProviderError("companies-house", domain.ClassNotFound, errNoMatch)
			}
			return p.Items[0].CompanyNumber, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/company/"+url.PathEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				CompanyName    string   `json:"company_name"`
				Type           string   `json:"type"`
				DateOfCreation string   `json:"date_of_creation"`
				SICCodes       []string `json:"sic_codes"`
				Office         struct {
					Locality string `json:"locality"`
					Country  string `json:"country"`
				} `json:"registered_office_address"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			// The fetch response is the terminal result — a single GET, always done.
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, p.CompanyName, 0.85)
			put(domain.FieldCompanyType, p.Type, 0.85)
			put(domain.FieldCompanyFoundedYear, yearOf(p.DateOfCreation), 0.85)
			put(domain.FieldSIC, normList(p.SICCodes), 0.85)
			put(domain.FieldCompanyHQCountry, p.Office.Country, 0.85)
			put(domain.FieldCompanyHQCity, p.Office.Locality, 0.85)
			return res, true, nil
		},
	}
}
