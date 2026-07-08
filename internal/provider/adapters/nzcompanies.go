package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// NZCompanies builds a match→fetch adapter for the New Zealand MBIE NZBN API v5 (docs/03 §7) — the
// official read path for Companies Office data (the sibling "Companies Register" API is
// write/maintenance-only behind 3-legged OAuth).
//   - Match: GET {base}/entities?search-term={company_name} → items[0].nzbn. No hits = HTTP 200 with
//     {"totalItems":0,"items":[]} (verified live) → NOT_FOUND.
//   - Fetch: GET {base}/entities/{nzbn} → the FullEntity (terminal).
//   - Auth: subscription key in the "Ocp-Apim-Subscription-Key" HEADER (Azure APIM), injected at
//     egress (AuthAPIKeyHeader); free registration + API Access Agreement.
//   - base default https://api.business.govt.nz/gateway/nzbn/v5 [portal.api.business.govt.nz/api/nzbn].
//   - Fills company_name (entityName), company_type (entityTypeDescription), company_founded_year
//     (year of registrationDate — may postdate actual founding for re-registered entities),
//     company_hq_country/city (REGISTERED address countryCode / address3 — city rides in address3 by
//     NZBN convention, ~0.65), industry (industryClassifications[0].classificationDescription —
//     ANZSIC 2006; the code is NOT mapped to naics/sic), company_domain (websites[0].url →
//     bareDomain), company_phone (phoneNumbers[0].phoneNumber).
//   - Quirk: ETag/304 conditional GETs (not used here); Wed 21:00-23:00 NZ maintenance window.
//
// VERIFIED from the official portal + live register responses (search + full entity for Xero):
// endpoints, header name, field names verbatim. Optional (Schedule 4) fields are often absent.
func NZCompanies(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.business.govt.nz/gateway/nzbn/v5"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "nz-companies",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "Ocp-Apim-Subscription-Key", KeyPoolSelector: "nz-companies:default"},
		Policy:       provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		PollInterval: 1 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 0, ExpectedConfidence: 0.92},
			{Field: domain.FieldCompanyType, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyFoundedYear, Cost: 0, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCountry, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCity, Cost: 0, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 0, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 0, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyPhone, Cost: 0, ExpectedConfidence: 0.70},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/entities")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("search-term", req.Known[domain.FieldCompanyName])
			q.Set("page-size", "1")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Items []struct {
					NZBN string `json:"nzbn"`
				} `json:"items"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if len(p.Items) == 0 || p.Items[0].NZBN == "" {
				return "", domain.NewProviderError("nz-companies", domain.ClassNotFound, errNoMatch)
			}
			return p.Items[0].NZBN, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet,
				strings.TrimRight(base, "/")+"/entities/"+url.PathEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				EntityName            string `json:"entityName"`
				EntityTypeDescription string `json:"entityTypeDescription"`
				RegistrationDate      string `json:"registrationDate"`
				Addresses             struct {
					AddressList []struct {
						Address3    string `json:"address3"`
						CountryCode string `json:"countryCode"`
						AddressType string `json:"addressType"`
					} `json:"addressList"`
				} `json:"addresses"`
				IndustryClassifications []struct {
					ClassificationDescription string `json:"classificationDescription"`
				} `json:"industryClassifications"`
				Websites []struct {
					URL string `json:"url"`
				} `json:"websites"`
				PhoneNumbers []struct {
					PhoneNumber string `json:"phoneNumber"`
				} `json:"phoneNumbers"`
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
			put(domain.FieldCompanyName, p.EntityName, 0.92)
			put(domain.FieldCompanyType, p.EntityTypeDescription, 0.90)
			put(domain.FieldCompanyFoundedYear, yearOf(p.RegistrationDate), 0.85)
			// Prefer the REGISTERED address; fall back to the first entry.
			for i, a := range p.Addresses.AddressList {
				if a.AddressType == "REGISTERED" || i == 0 {
					put(domain.FieldCompanyHQCountry, a.CountryCode, 0.90)
					put(domain.FieldCompanyHQCity, a.Address3, 0.65)
					if a.AddressType == "REGISTERED" {
						break
					}
				}
			}
			if len(p.IndustryClassifications) > 0 {
				put(domain.FieldIndustry, p.IndustryClassifications[0].ClassificationDescription, 0.85)
			}
			if len(p.Websites) > 0 {
				put(domain.FieldCompanyDomain, bareDomain(p.Websites[0].URL), 0.75)
			}
			if len(p.PhoneNumbers) > 0 {
				put(domain.FieldCompanyPhone, p.PhoneNumbers[0].PhoneNumber, 0.70)
			}
			return res, true, nil
		},
	}
}
