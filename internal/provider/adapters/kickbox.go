package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Kickbox builds an adapter for the Kickbox v2 email verification API (docs/03).
//   - Endpoint: GET {base}?email=  (base default https://api.kickbox.com/v2/verify).
//   - Auth: API key in the "apikey" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status ("deliverable"|"undeliverable"|"risky"|"unknown");
//     per-value confidence uses Kickbox's `sendex` quality score (0..1) when present.
//   - No special status quirk beyond the standard map.
//
// VERIFIED from docs: endpoint, `apikey` query auth, result vocabulary, sendex 0..1. Exact JSON
// field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Kickbox(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.kickbox.com/v2/verify"
	}
	return &provider.HTTPAdapter{
		NameV:   "kickbox",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "apikey",
			KeyPoolSelector: "kickbox:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("email", req.Known[domain.FieldWorkEmail])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Result  string  `json:"result"` // "deliverable" | "undeliverable" | "risky" | "unknown"
				Sendex  float64 `json:"sendex"` // 0..1 quality score
				Success bool    `json:"success"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				conf := domain.Confidence(0.90)
				if p.Sendex > 0 {
					conf = domain.Confidence(p.Sendex).Clamp()
				}
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: conf}
			}
			return res, nil
		},
	}
}
