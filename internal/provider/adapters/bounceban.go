package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// BounceBan builds an adapter for the BounceBan single email verification API (docs/03 §7),
// catch-all specialist. Uses the SYNCHRONOUS waterfall host (blocks and returns the verdict inline).
//   - Endpoint: GET {base}/v1/verify/single?email=  (base default https://api-waterfall.bounceban.com)
//     [bounceban.com/public/doc].
//   - Auth: API key in the "Authorization" header with NO "Bearer" prefix (raw), injected at egress.
//   - Input: work_email. Fills email_status from `result` (deliverable/risky/undeliverable/unknown).
//   - Quirk: real HTTP codes — 401 auth, 403 = insufficient credits (the shared map treats 403 as
//     RATE_LIMIT; both mean "back off / key issue"). No name fields returned.
//
// VERIFIED from docs: waterfall host, raw-Authorization auth, /v1/verify/single, `result` enum.
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func BounceBan(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api-waterfall.bounceban.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "bounceban",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "Authorization", KeyPoolSelector: "bounceban:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/v1/verify/single")
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
				Result string `json:"result"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
