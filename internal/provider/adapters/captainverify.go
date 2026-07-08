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

// CaptainVerify builds an adapter for the CaptainVerify single-email verification API (docs/03 §9).
//   - Endpoint: GET {base}/v2/verify?email=  (base default https://api.captainverify.com)
//     [captainverify.com/api.html].
//   - Auth: API key in the "apikey" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (result: valid|invalid|risky|unknown).
//   - Quirk: errors are HTTP 200 with success=false + a message (no distinct HTTP code documented) —
//     Decode classifies the message; credit exhaustion is NOT an error, it degrades result to
//     "unknown" (and is not billed). Adapter checks the success flag on every 200.
//
// VERIFIED from docs: endpoint, apikey query auth, result enum, success/message error convention.
// Exact field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func CaptainVerify(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.captainverify.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "captainverify",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "apikey",
			KeyPoolSelector: "captainverify:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/v2/verify")
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
				Result  string `json:"result"`
				Success *bool  `json:"success"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body: success=false carries an auth/credit message.
			if p.Success != nil && !*p.Success {
				return provider.Result{}, bodyErr("captainverify", p.Message)
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
