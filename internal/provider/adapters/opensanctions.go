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

// OpenSanctions builds an adapter for the OpenSanctions (yente) match API (docs/03 §7) — a
// sanctions/PEP/watchlist SCREENING source, not a general firmographics provider: it returns data
// only for risk-listed entities, so a no-hit is the expected result for ordinary B2B subjects.
//   - Endpoint: POST {base}/match/default with FollowTheMoney body
//     {"queries":{"q":{"schema":"Company","properties":{"name":["…"],"country":["…"]}}}}
//     (property values are ARRAYS; schema is a constant) [opensanctions.org/docs/api, openapi.json].
//   - Auth: "Authorization: ApiKey <key>" — the literal "ApiKey " prefix, NOT Bearer, so this is
//     AuthAPIKeyHeader on "Authorization" and the "<slug>:default" pool secret must hold the full
//     value "ApiKey <key>" (egress sets the header verbatim).
//   - Input: company_name (+company_hq_country). Fills company_name (caption) + company_hq_country
//     (properties.country[0], lowercase ISO — country of association, not strictly HQ) — only when
//     the API itself asserts match==true; confidence is scaled by the returned score.
//   - Status: DEPRIORITIZED (ADR-0009) — public-records provenance (government sanctions/PEP lists);
//     useful as an optional compliance screen, not a waterfall firmographics source.
//   - Quirk: outer HTTP 200 always; per-query pseudo-status at responses.q.status; zero candidates =
//     200 with results:[] (only 200s are billed). 429 = monthly quota (calendar-month, QUOTA not
//     rate-limit; the shared map treats 429 as RATE_LIMIT — documented discrepancy).
//
// VERIFIED from official docs + openapi.json: endpoint, ApiKey header form, query/response shapes,
// score/match fields. Exact field names pinned UNVERIFIED until a live key (see hunter.go).
func OpenSanctions(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.opensanctions.org"
	}
	return &provider.HTTPAdapter{
		NameV:   "opensanctions",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization",
			KeyPoolSelector: "opensanctions:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCountry, Cost: 1, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			props := map[string][]string{}
			if v := req.Known[domain.FieldCompanyName]; v != "" {
				props["name"] = []string{v}
			}
			if v := req.Known[domain.FieldCompanyHQCountry]; v != "" {
				props["country"] = []string{strings.ToLower(v)}
			}
			payload, err := json.Marshal(map[string]any{
				"queries": map[string]any{
					"q": map[string]any{"schema": "Company", "properties": props},
				},
			})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/match/default", bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Responses struct {
					Q struct {
						Results []struct {
							Caption    string  `json:"caption"`
							Score      float64 `json:"score"`
							Match      bool    `json:"match"`
							Properties struct {
								Country []string `json:"country"`
							} `json:"properties"`
						} `json:"results"`
					} `json:"q"`
				} `json:"responses"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			// Only accept what the API itself asserts as a match; scale confidence by its score.
			for _, r := range p.Responses.Q.Results {
				if !r.Match {
					continue
				}
				if r.Caption != "" {
					res.Values[domain.FieldCompanyName] = provider.Observation{
						Value: r.Caption, Confidence: domain.Confidence(0.9 * r.Score).Clamp()}
				}
				if len(r.Properties.Country) > 0 && r.Properties.Country[0] != "" {
					res.Values[domain.FieldCompanyHQCountry] = provider.Observation{
						Value: strings.ToUpper(r.Properties.Country[0]), Confidence: domain.Confidence(0.8 * r.Score).Clamp()}
				}
				break
			}
			return res, nil
		},
	}
}
