package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// PredictLeads builds an adapter for the PredictLeads v3 company API (docs/03 §7) — company signals
// & technographics. Single-shot GET by domain.
//   - Endpoint: GET {base}/companies/{domain}  (base default https://predictleads.com/api/v3)
//     [docs.predictleads.com/v3].
//   - Auth: TWO credential headers — X-Api-Key + X-Api-Token (ADR-0024 Phase 4b AuthAPIKeyDualHeader);
//     the pool secret is "apiKey:apiToken", split and injected at egress. Adapter holds neither.
//   - Fills company_name, company_domain, company_hq_country from the company record. (industry,
//     employee_count, intent_topics are NOT in the company model; technographics + buying_signal
//     live on separate /technology_detections + /news_events endpoints — a future multi-call pass.)
//
// VERIFIED from docs: base, dual-header auth, GET /companies/{id_or_domain}, company.attributes.
// {company_name,domain,location_data[category=headquarters].country}. Field names pinned UNVERIFIED
// until a live authorized call (see hunter.go). First consumer of AuthAPIKeyDualHeader.
func PredictLeads(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://predictleads.com/api/v3"
	}
	return &provider.HTTPAdapter{
		NameV:   "predictleads",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:           provider.AuthAPIKeyDualHeader,
			HeaderName:       "X-Api-Key",
			SecondHeaderName: "X-Api-Token",
			KeyPoolSelector:  "predictleads:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 4, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCountry, Cost: 4, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := base + "/companies/" + url.PathEscape(req.Known[domain.FieldCompanyDomain])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					Attributes struct {
						CompanyName  string `json:"company_name"`
						Domain       string `json:"domain"`
						LocationData []struct {
							Category string `json:"category"`
							Country  string `json:"country"`
						} `json:"location_data"`
					} `json:"attributes"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			a := p.Data.Attributes
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, a.CompanyName, 0.85)
			put(domain.FieldCompanyDomain, a.Domain, 0.90)
			for _, l := range a.LocationData {
				if l.Category == "headquarters" {
					put(domain.FieldCompanyHQCountry, l.Country, 0.75)
					break
				}
			}
			return res, nil
		},
	}
}
