package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Wappalyzer builds an adapter for the Wappalyzer v2 Technology Lookup API (docs/03 §7).
//   - Endpoint: GET {base}?urls=https://{domain}  (base default https://api.wappalyzer.com/v2/lookup/)
//     [wappalyzer.com/docs/api/v2/lookup].
//   - Auth: API key in the "x-api-key" header, injected at egress (AuthAPIKeyHeader).
//   - Input: company_domain (turned into an https URL). Fills: technographics (sorted, comma-joined
//     detected technology names) — crowdsourced frontend tech detection.
//   - Response is a TOP-LEVEL JSON ARRAY (one element per looked-up URL); the adapter reads the
//     first element. No match / unknown domain returns an empty technologies array (NOT_FOUND).
//
// VERIFIED from docs: endpoint, x-api-key auth, `technologies[].name`. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func Wappalyzer(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.wappalyzer.com/v2/lookup/"
	}
	return &provider.HTTPAdapter{
		NameV:   "wappalyzer",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "x-api-key",
			KeyPoolSelector: "wappalyzer:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.85},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("urls", "https://"+req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p []struct {
				Technologies []struct {
					Name string `json:"name"`
				} `json:"technologies"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p) == 0 {
				return res, nil
			}
			techs := make([]string, 0, len(p[0].Technologies))
			for _, t := range p[0].Technologies {
				techs = append(techs, t.Name)
			}
			if v := normList(techs); v != "" {
				res.Values[domain.FieldTechnographics] = provider.Observation{Value: v, Confidence: 0.85}
			}
			return res, nil
		},
	}
}
