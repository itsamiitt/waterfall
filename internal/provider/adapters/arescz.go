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

// AresCZ builds an adapter for the Czech ARES economic-subjects registry (docs/03 §7) — the official
// Ministry of Finance open-data business register.
//   - Endpoint: POST {base}/ekonomicke-subjekty/vyhledat with JSON {"obchodniJmeno":name,"pocet":1}
//     (base default https://ares.gov.cz/ekonomicke-subjekty-v-be/rest) [ares.gov.cz OpenAPI, live-verified].
//   - Auth: NONE — the spec declares Basic/Bearer schemes but they are vestigial; live unauthenticated
//     calls return full data (AuthNone; egress passthrough).
//   - Input: company_name. Fills company_name (obchodniJmeno), company_hq_country (sidlo.nazevStatu),
//     company_hq_city (sidlo.nazevObce), company_type (pravniForma — a 3-digit legal-form CODE),
//     company_founded_year (year of datumVzniku), industry (czNace[] CZ-NACE codes → normalized; NOT
//     NAICS/SIC, so not mapped there).
//   - Quirk: search wraps entities in "ekonomickeSubjekty"[]; no-match GET-by-ICO returns a
//     {"kod":"NENALEZENO"} body (the search returns an empty array). Coded fields (pravniforma/czNace)
//     need a separate /ciselniky-nazevniky lookup to render text — stored as codes here.
//
// VERIFIED from the official OpenAPI spec + live public calls: base, no-auth, entity field names.
// The vyhledat response envelope key ("ekonomickeSubjekty") is the standard ARES v3 key.
func AresCZ(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://ares.gov.cz/ekonomicke-subjekty-v-be/rest"
	}
	return &provider.HTTPAdapter{
		NameV:   "ares-cz",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthNone},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCountry, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCity, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyType, Cost: 0, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyFoundedYear, Cost: 0, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 0, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload, err := json.Marshal(map[string]any{
				"obchodniJmeno": req.Known[domain.FieldCompanyName],
				"pocet":         1,
			})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/ekonomicke-subjekty/vyhledat", bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				EkonomickeSubjekty []struct {
					ObchodniJmeno string `json:"obchodniJmeno"`
					Sidlo         struct {
						NazevStatu string `json:"nazevStatu"`
						NazevObce  string `json:"nazevObce"`
					} `json:"sidlo"`
					PravniForma string   `json:"pravniForma"`
					DatumVzniku string   `json:"datumVzniku"`
					CzNace      []string `json:"czNace"`
				} `json:"ekonomickeSubjekty"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.EkonomickeSubjekty) == 0 {
				return res, nil
			}
			e := p.EkonomickeSubjekty[0]
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, e.ObchodniJmeno, 0.90)
			put(domain.FieldCompanyHQCountry, e.Sidlo.NazevStatu, 0.90)
			put(domain.FieldCompanyHQCity, e.Sidlo.NazevObce, 0.90)
			put(domain.FieldCompanyType, e.PravniForma, 0.75)
			put(domain.FieldCompanyFoundedYear, yearOf(e.DatumVzniku), 0.85)
			put(domain.FieldIndustry, normList(e.CzNace), 0.80)
			return res, nil
		},
	}
}
