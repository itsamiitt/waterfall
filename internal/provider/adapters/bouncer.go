package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Bouncer builds an adapter for the Bouncer real-time email verification API (docs/03 §7).
//   - Endpoint: GET {base}?email=  (base default https://api.usebouncer.com/v1.1/email/verify)
//     [docs.usebouncer.com/api-reference/real-time/verify-email].
//   - Auth: API key in the "x-api-key" header, injected at egress (AuthAPIKeyHeader).
//   - Input: work_email. Fills: email_status (deliverable|risky|undeliverable|unknown).
//   - Standard status codes (no 200-with-error-body). No match = 200 with status=undeliverable.
//
// VERIFIED from docs: endpoint, x-api-key auth, `status` verdict + `score`. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func Bouncer(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.usebouncer.com/v1.1/email/verify"
	}
	return &provider.HTTPAdapter{
		NameV:   "bouncer",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "x-api-key",
			KeyPoolSelector: "bouncer:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
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
				Status string `json:"status"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Status != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Status, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
