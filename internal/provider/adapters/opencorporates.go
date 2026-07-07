package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// OpenCorporates builds an adapter for the OpenCorporates company registry search (docs/03 §7).
//   - Endpoint: GET {base}?q=  (base default https://api.opencorporates.com/v0.4/companies/search)
//     [api.opencorporates.com/documentation/API-Reference].
//   - Auth: API token in the "api_token" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: company_name (search q). Fills official-registry firmographics: name, incorporation
//     year, jurisdiction (as hq country), legal company_type, registered-address city.
//   - Quirk: zero matches = HTTP 200 with an empty results.companies array (NOT 404); 403 is used
//     for rate-limit exhaustion (default 403→RATE_LIMIT already matches).
//
// VERIFIED from docs: endpoint, api_token auth, results.companies[].company shape, status codes.
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func OpenCorporates(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.opencorporates.com/v0.4/companies/search"
	}
	return &provider.HTTPAdapter{
		NameV:   "opencorporates",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api_token",
			KeyPoolSelector: "opencorporates:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("q", req.Known[domain.FieldCompanyName])
			if jc := req.Known[domain.FieldCompanyHQCountry]; jc != "" {
				q.Set("jurisdiction_code", jc)
			}
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Results struct {
					Companies []struct {
						Company struct {
							Name              string `json:"name"`
							IncorporationDate string `json:"incorporation_date"`
							JurisdictionCode  string `json:"jurisdiction_code"`
							CompanyType       string `json:"company_type"`
							RegisteredAddress struct {
								Locality string `json:"locality"`
							} `json:"registered_address"`
						} `json:"company"`
					} `json:"companies"`
				} `json:"results"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Results.Companies) == 0 {
				return res, nil
			}
			c := p.Results.Companies[0].Company
			put := func(f domain.Field, v string, conf domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: conf}
				}
			}
			put(domain.FieldCompanyName, c.Name, 0.85)
			put(domain.FieldCompanyFoundedYear, yearOf(c.IncorporationDate), 0.85)
			put(domain.FieldCompanyHQCountry, c.JurisdictionCode, 0.80)
			put(domain.FieldCompanyType, c.CompanyType, 0.85)
			put(domain.FieldCompanyHQCity, c.RegisteredAddress.Locality, 0.75)
			return res, nil
		},
	}
}
