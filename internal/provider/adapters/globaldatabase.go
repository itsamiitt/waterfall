package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// GlobalDatabase builds an adapter for the Global Database v2 URL-enrichment API (docs/03 §7) —
// official company-registry firmographics.
//   - Endpoint: POST {base} with JSON {url}  (base default
//     https://api.globaldatabase.com/v2/enrichment/url) [api.globaldatabase.com/docs/v2].
//   - Auth: "Authorization: Token <key>" — the key-pool secret must be stored WITH the "Token "
//     prefix, injected at egress (AuthAPIKeyHeader on "Authorization").
//   - Input: company_domain (url). Fills registry firmographics (name, domain, phone, size, founded
//     year, industry, SIC, country/city, legal form, LinkedIn).
//   - NOTE: registration_number/vat_number are national-registry IDs, NOT D&B DUNS — not mapped.
//
// VERIFIED from docs + a third-party listing: endpoint, "Token" auth, flat response fields + the
// industry[]/sic[] {value} arrays. Revenue (Turnover) nesting is UNVERIFIED so not mapped; field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func GlobalDatabase(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.globaldatabase.com/v2/enrichment/url"
	}
	return &provider.HTTPAdapter{
		NameV:   "global-database",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization", // secret stored as "Token <key>"
			KeyPoolSelector: "global-database:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			b, err := json.Marshal(map[string]string{"url": req.Known[domain.FieldCompanyDomain]})
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
				Name           string   `json:"name"`
				CompanyWebsite string   `json:"company_website"`
				CompanyPhone   []string `json:"company_phone"`
				Size           string   `json:"size"`
				FoundingDate   string   `json:"founding_date"`
				CountryName    string   `json:"country_name"`
				AddressCity    string   `json:"address_city"`
				LegalForm      string   `json:"company_legal_form"`
				LinkedIn       string   `json:"linkedin"`
				Industry       []struct {
					Value string `json:"value"`
				} `json:"industry"`
				SIC []struct {
					Value string `json:"value"`
				} `json:"sic"`
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
			put(domain.FieldCompanyName, p.Name, 0.90)
			put(domain.FieldCompanyDomain, bareDomain(p.CompanyWebsite), 0.85)
			put(domain.FieldEmployeeCount, p.Size, 0.80)
			put(domain.FieldCompanyFoundedYear, yearOf(p.FoundingDate), 0.90)
			put(domain.FieldCompanyHQCountry, p.CountryName, 0.90)
			put(domain.FieldCompanyHQCity, p.AddressCity, 0.85)
			put(domain.FieldCompanyType, p.LegalForm, 0.90)
			if len(p.CompanyPhone) > 0 {
				put(domain.FieldCompanyPhone, p.CompanyPhone[0], 0.85)
			}
			if len(p.Industry) > 0 {
				put(domain.FieldIndustry, p.Industry[0].Value, 0.75)
			}
			if len(p.SIC) > 0 {
				put(domain.FieldSIC, p.SIC[0].Value, 0.85)
			}
			if p.LinkedIn != "" {
				li := p.LinkedIn
				if !strings.HasPrefix(li, "http") {
					li = "https://" + li
				}
				put(domain.FieldCompanyLinkedInURL, li, 0.75)
			}
			return res, nil
		},
	}
}
