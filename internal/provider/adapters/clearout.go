package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Clearout builds an adapter for the Clearout instant email-verify API (docs/03 §7).
//   - Endpoint: POST {base} with JSON {email}  (base default
//     https://api.clearout.io/v2/email_verify/instant) [docs.clearout.io/developers/api/email-verify].
//   - Auth: Bearer token, injected at egress (AuthBearer). NOTE: Clearout's convention may use the
//     colon form "Authorization: Bearer:<token>" (no space); the egress injector uses the standard
//     space form. UNVERIFIED — a localized Build change if a live call 401s.
//   - Input: work_email. Fills: email_status (valid|invalid|catch_all|unknown from data.status).
//   - Quirk: an invalid/expired token or out-of-credits can be returned as HTTP 200 with top-level
//     `status:"failure"` and an `error{code,message}` body. Decode branches on `status` and
//     classifies error.message via classifyErrMsg.
//
// VERIFIED from docs: endpoint, Bearer auth, {status,data:{status}} success shape, status verdicts.
// Auth header form + exact error body pinned UNVERIFIED until a live authorized call (see hunter.go).
func Clearout(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.clearout.io/v2/email_verify/instant"
	}
	return &provider.HTTPAdapter{
		NameV:   "clearout",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "clearout:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			b, err := json.Marshal(map[string]string{"email": req.Known[domain.FieldWorkEmail]})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Status string `json:"status"`
				Data   struct {
					Status string `json:"status"`
				} `json:"data"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			if p.Status == "failure" {
				return provider.Result{}, domain.NewProviderError("clearout", classifyErrMsg(p.Error.Message), errors.New(p.Error.Message))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Data.Status != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Data.Status, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
