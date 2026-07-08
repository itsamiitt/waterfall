package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Reoon builds an adapter for the Reoon Email Verifier single-verify API (docs/03 §9).
//   - Endpoint: GET {base}/verify?email=&mode=power  (base default https://emailverifier.reoon.com/api/v1)
//     [reoon.com/articles/api-documentation-of-reoon-email-verifier].
//   - Auth: API key in the "key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (power-mode enum: safe|invalid|disabled|disposable|
//     inbox_full|catch_all|role_account|spamtrap|unknown) + work_email (echoed address).
//   - Quirk: 200-with-error-body — errors come as {"status":"error","reason":"…"}; Decode classifies
//     the reason (credit→QUOTA, rate→RATE_LIMIT, else AUTH). "unknown" is a soft/indeterminate
//     verdict, not an error.
//
// VERIFIED from docs: endpoint, key query auth, mode param, status enums, the error body shape.
// Exact field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Reoon(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://emailverifier.reoon.com/api/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "reoon",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "key",
			KeyPoolSelector: "reoon:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/verify")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("email", req.Known[domain.FieldWorkEmail])
			q.Set("mode", "power")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email  string `json:"email"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body: {"status":"error","reason":"..."}.
			if p.Status == "error" {
				return provider.Result{}, bodyErr("reoon", p.Reason)
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Status != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Status, Confidence: 0.90}
			}
			if p.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Email, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
