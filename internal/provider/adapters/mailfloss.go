package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Mailfloss builds an adapter for the Mailfloss v1 real-time email verification API (docs/03 §7).
//   - Endpoint: GET {base}/v1/verify?email=  (base default https://api.mailfloss.com)
//     [api.mailfloss.com/v1/openapi.json].
//   - Auth: Bearer token (mf_live_… keys), injected at egress.
//   - Input: work_email. Fills email_status from `status` (passed/undeliverable/risky/unknown).
//     (Batch verification is a separate submit-poll flow, not modeled here.)
//
// VERIFIED from the live OpenAPI spec: base, Bearer auth, GET /v1/verify, response {email,domain,
// status,reason,passed,role,disposable,free,suggestion}. `domain` is the mailbox domain (not a
// company mapping) so it is not mapped. Field names pinned UNVERIFIED until a live call (hunter.go).
func Mailfloss(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.mailfloss.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "mailfloss",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "mailfloss:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.88},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/v1/verify")
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
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Status, Confidence: 0.88}
			}
			return res, nil
		},
	}
}
