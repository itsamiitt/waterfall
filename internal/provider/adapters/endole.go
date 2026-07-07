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

// Endole builds an async match→fetch adapter for the Endole UK company-registry API (docs/03 §7).
//   - Match: GET {base}/search/companies?q={company_name} → items[0].company_number (none ⇒ NOT_FOUND).
//   - Fetch: GET {base}/company/{company_number} → the Companies-House-shaped profile (done on fetch).
//   - Auth: HTTP Basic (pool secret "appId:appKey"), injected at egress.
//   - base default https://api.endole.co.uk [endole.co.uk/developers].
//
// VERIFIED from docs: search/company endpoints, Basic auth, profile fields company_name/type/
// date_of_creation/sic_codes/registered_office_address.{locality,country}. Registry-sourced
// (Companies House) ⇒ ACTIVE-CANDIDATE. Field names pinned UNVERIFIED (see hunter.go).
func Endole(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.endole.co.uk"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "endole",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBasic, KeyPoolSelector: "endole:default"},
		Policy:       provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		PollInterval: time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.95},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.88},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.85},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := base + "/search/companies?q=" + url.QueryEscape(req.Known[domain.FieldCompanyName])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
				return "", domain.NewProviderError("endole", domain.ClassNotFound, errResultsGone)
			}
			return p.Items[0].CompanyNumber, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/company/"+url.PathEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				CompanyName      string   `json:"company_name"`
				Type             string   `json:"type"`
				DateOfCreation   string   `json:"date_of_creation"`
				SICCodes         []string `json:"sic_codes"`
				RegisteredOffice struct {
					Locality string `json:"locality"`
					Country  string `json:"country"`
				} `json:"registered_office_address"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, p.CompanyName, 0.95)
			put(domain.FieldCompanyType, p.Type, 0.85)
			put(domain.FieldCompanyFoundedYear, yearOf(p.DateOfCreation), 0.88)
			put(domain.FieldCompanyHQCountry, p.RegisteredOffice.Country, 0.90)
			put(domain.FieldCompanyHQCity, p.RegisteredOffice.Locality, 0.85)
			if len(p.SICCodes) > 0 {
				put(domain.FieldSIC, p.SICCodes[0], 0.90)
			}
			return res, true, nil
		},
	}
}
