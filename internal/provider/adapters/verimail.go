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

// Verimail builds an adapter for the Verimail v3 single-email verification API (docs/03 §9).
//   - Endpoint: GET {base}/v3/verify?email=  (base default https://api.verimail.io)
//     [verimail.io/docs/v3].
//   - Auth: API key in the "key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (result: deliverable|inbox_full|hardbounce|softbounce|
//     catch_all|disposable|undeliverable|unknown) + work_email (echoed address).
//   - Quirk: the body carries its own "status" field (success|error) that must be checked
//     independently of the HTTP code; result="unknown" is a soft outcome (network security block),
//     not an error. 403 = key inactive due to exceeded quota/overdue payment (QUOTA — the shared
//     map treats 403 as RATE_LIMIT; documented discrepancy).
//
// VERIFIED from docs: endpoint, key query auth, result enum, status field, error codes. Exact field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Verimail(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.verimail.io"
	}
	return &provider.HTTPAdapter{
		NameV:   "verimail",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "key",
			KeyPoolSelector: "verimail:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/v3/verify")
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
				Status  string `json:"status"`
				Message string `json:"message"`
				Email   string `json:"email"`
				Result  string `json:"result"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// In-body status must be checked independently of the HTTP code.
			if p.Status == "error" {
				return provider.Result{}, bodyErr("verimail", p.Message)
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			if p.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Email, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
