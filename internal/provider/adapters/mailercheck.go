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

// MailerCheck builds an adapter for the MailerCheck real-time single verify API (docs/03 §9).
//   - Endpoint: POST {base}/check/single with JSON {email}  (base default https://app.mailercheck.com/api)
//     [developers.mailercheck.com/email].
//   - Auth: bearer token "Authorization: Bearer <token>", injected at egress (AuthBearer).
//   - Input: work_email. Fills: email_status — the body is minimal, a single "status" string (valid|
//     catch_all|mailbox_full|role|unknown|syntax_error|typo|mailbox_not_found|disposable|blocked).
//   - Quirk: 429 = 60 req/min default across all endpoints (retry-after header); 422 = body failed
//     validation. An async variant (/check/single-async submit→poll) exists; the real-time endpoint
//     is the primary path here.
//
// VERIFIED from docs: endpoint, bearer auth, status vocabulary, rate limit. Exact field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func MailerCheck(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://app.mailercheck.com/api"
	}
	return &provider.HTTPAdapter{
		NameV:   "mailercheck",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "mailercheck:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload, err := json.Marshal(map[string]string{"email": req.Known[domain.FieldWorkEmail]})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/check/single", bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
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
