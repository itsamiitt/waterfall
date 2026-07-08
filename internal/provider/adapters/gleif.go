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

// GLEIF builds an adapter for the GLEIF LEI Records API (docs/03 §7) — the official, free, global
// Legal Entity Identifier registry.
//   - Endpoint: GET {base}/lei-records?filter[entity.legalName]={name}&page[size]=1  (base default
//     https://api.gleif.org/api/v1; JSON:API) [gleif.org/en/lei-data/gleif-api, live-verified].
//   - Auth: NONE — fully public, no key or registration (AuthNone; egress passthrough). 60 req/min.
//   - Input: company_name. Fills: company_name (legalName.name, corroborated registry data),
//     company_hq_country (headquartersAddress.country, ISO alpha-2), company_hq_city,
//     company_type (legalForm.id — an ISO 20275 ELF CODE like "T91T", not a label; documented),
//     company_founded_year (entity.creationDate, optional/often null → lowered prior 0.60).
//   - Quirk: no-match = HTTP 200 with empty data:[] (verified live) → Decode omits fields
//     (NOT_FOUND semantics). 406 (HTML body) on unsupported Accept — Build sends none, which works.
//     GLEIF carries no employee/industry/revenue data.
//
// VERIFIED from official docs + live public calls (open-data API — live reads authorized by
// design): endpoint, no-auth, JSON:API paths, empty-data no-match.
func GLEIF(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.gleif.org/api/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "gleif",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthNone},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 0, ExpectedConfidence: 0.93},
			{Field: domain.FieldCompanyHQCountry, Cost: 0, ExpectedConfidence: 0.92},
			{Field: domain.FieldCompanyHQCity, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyType, Cost: 0, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyFoundedYear, Cost: 0, ExpectedConfidence: 0.60},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/lei-records")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("filter[entity.legalName]", req.Known[domain.FieldCompanyName])
			q.Set("page[size]", "1")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data []struct {
					Attributes struct {
						Entity struct {
							LegalName struct {
								Name string `json:"name"`
							} `json:"legalName"`
							HeadquartersAddress struct {
								City    string `json:"city"`
								Country string `json:"country"`
							} `json:"headquartersAddress"`
							LegalForm struct {
								ID string `json:"id"`
							} `json:"legalForm"`
							CreationDate string `json:"creationDate"`
						} `json:"entity"`
					} `json:"attributes"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Data) == 0 {
				return res, nil // empty data[] = no match (verified live)
			}
			e := p.Data[0].Attributes.Entity
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, e.LegalName.Name, 0.93)
			put(domain.FieldCompanyHQCountry, e.HeadquartersAddress.Country, 0.92)
			put(domain.FieldCompanyHQCity, e.HeadquartersAddress.City, 0.90)
			put(domain.FieldCompanyType, e.LegalForm.ID, 0.70)
			put(domain.FieldCompanyFoundedYear, yearOf(e.CreationDate), 0.60)
			return res, nil
		},
	}
}
