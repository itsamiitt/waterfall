package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// FullContact builds an adapter for the FullContact v3 Company Enrich API (docs/03 §7).
//   - Endpoint: POST {base} with JSON {"domain":..}  (base default
//     https://api.fullcontact.com/v3/company.enrich) [docs.fullcontact.com/docs/company-enrich-overview].
//   - Auth: Bearer token, injected at egress (AuthBearer).
//   - Input: company_domain. Fills: company_name, company_domain, LinkedIn, employees, founded year,
//     industry, HQ city/country, company_phone, and SIC/NAICS from details.industries[].
//   - Status: DEPRIORITIZED (ADR-0009) — social/public-web provenance. Quirk: 403 conflates
//     invalid-key AND quota (docs use no 401/402); the shared map treats 403 as RATE_LIMIT, so a
//     bad key backs off rather than being disabled — noted UNVERIFIED. 404 = no match (24h cache).
//
// VERIFIED from docs: endpoint, Bearer auth, {domain} body, top-level + details.* response shape.
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func FullContact(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.fullcontact.com/v3/company.enrich"
	}
	return &provider.HTTPAdapter{
		NameV:   "fullcontact",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "fullcontact:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 6, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 6, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyFoundedYear, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldIndustry, Cost: 6, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCity, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCountry, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyPhone, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldSIC, Cost: 6, ExpectedConfidence: 0.85},
			{Field: domain.FieldNAICS, Cost: 6, ExpectedConfidence: 0.85},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			b, err := json.Marshal(map[string]string{"domain": req.Known[domain.FieldCompanyDomain]})
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
				Name      string `json:"name"`
				Website   string `json:"website"`
				LinkedIn  string `json:"linkedin"`
				Founded   int64  `json:"founded"`
				Employees int64  `json:"employees"`
				Category  string `json:"category"`
				Details   struct {
					Industries []struct {
						Type string `json:"type"`
						Code string `json:"code"`
					} `json:"industries"`
					Phones []struct {
						Value string `json:"value"`
					} `json:"phones"`
					Locations []struct {
						City    string `json:"city"`
						Country string `json:"country"`
					} `json:"locations"`
				} `json:"details"`
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
			put(domain.FieldCompanyDomain, bareDomain(p.Website), 0.75)
			put(domain.FieldCompanyLinkedInURL, p.LinkedIn, 0.80)
			put(domain.FieldIndustry, p.Category, 0.65)
			if p.Employees > 0 {
				put(domain.FieldEmployeeCount, itoa(p.Employees), 0.65)
			}
			if p.Founded > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.Founded), 0.80)
			}
			if len(p.Details.Locations) > 0 {
				put(domain.FieldCompanyHQCity, p.Details.Locations[0].City, 0.80)
				put(domain.FieldCompanyHQCountry, p.Details.Locations[0].Country, 0.80)
			}
			if len(p.Details.Phones) > 0 {
				put(domain.FieldCompanyPhone, p.Details.Phones[0].Value, 0.70)
			}
			for _, ind := range p.Details.Industries {
				switch ind.Type {
				case "SIC":
					put(domain.FieldSIC, ind.Code, 0.85)
				case "NAICS":
					put(domain.FieldNAICS, ind.Code, 0.85)
				}
			}
			return res, nil
		},
	}
}
