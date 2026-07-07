package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// DataAxle builds an adapter for the Data Axle Places (business) Match API v2 (docs/03 §7) —
// compiled US/Canada business firmographics.
//   - Endpoint: POST {base} with JSON {"identifiers":{…}}  (base default
//     https://api.data-axle.com/v2/places/match) [platform.data-axle.com/places/docs/match_api_v2].
//   - Auth: API token in the "X-AUTH-TOKEN" header, injected at egress (AuthAPIKeyHeader).
//   - Input: company_name / company_domain (website) / company_phone / company_hq_city /
//     company_hq_country. Fills firmographics from document.attributes.
//   - No match = HTTP 200 with `"document": null` — the adapter yields no values (not an error).
//
// VERIFIED from docs: endpoint, X-AUTH-TOKEN auth, identifiers input, {count,document{attributes}}
// shape. Only clearly-string attributes are mapped; modeled numeric estimates (employees/sales) and
// NAICS/SIC code-ids are omitted to avoid type guessing. Field names pinned UNVERIFIED (see hunter.go).
func DataAxle(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.data-axle.com/v2/places/match"
	}
	return &provider.HTTPAdapter{
		NameV:   "data-axle",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-AUTH-TOKEN",
			KeyPoolSelector: "data-axle:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.55},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.55},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.55},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			ids := map[string]string{}
			putIf(ids, "name", req.Known[domain.FieldCompanyName])
			putIf(ids, "website", req.Known[domain.FieldCompanyDomain])
			putIf(ids, "phone", req.Known[domain.FieldCompanyPhone])
			putIf(ids, "city", req.Known[domain.FieldCompanyHQCity])
			putIf(ids, "country_code", req.Known[domain.FieldCompanyHQCountry])
			b, err := json.Marshal(map[string]any{"identifiers": ids})
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
				Document *struct {
					Attributes struct {
						Name              string `json:"name"`
						Phone             string `json:"phone"`
						City              string `json:"city"`
						CountryCode       string `json:"country_code"`
						LinkedInURL       string `json:"linked_in_url"`
						PlaceType         string `json:"place_type"`
						OpenedForBusiness string `json:"opened_for_business_on"`
					} `json:"attributes"`
				} `json:"document"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Document == nil {
				return res, nil
			}
			a := p.Document.Attributes
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, a.Name, 0.65)
			put(domain.FieldCompanyPhone, a.Phone, 0.65)
			put(domain.FieldCompanyHQCity, a.City, 0.65)
			put(domain.FieldCompanyHQCountry, a.CountryCode, 0.65)
			put(domain.FieldCompanyLinkedInURL, a.LinkedInURL, 0.55)
			put(domain.FieldCompanyType, a.PlaceType, 0.55)
			put(domain.FieldCompanyFoundedYear, yearOf(a.OpenedForBusiness), 0.55)
			return res, nil
		},
	}
}
