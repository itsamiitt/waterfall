package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// DeBounce builds an adapter for the DeBounce single-validation API (docs/03 §7).
//   - Endpoint: GET {base}?email=  (base default https://api.debounce.io/v1/) [developers.debounce.com].
//   - Auth: API key in the "api" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (the debounce.result label, e.g. "Safe to Send"|
//     "Invalid"|"Risky"|"Unknown").
//   - Quirk: the live endpoint returns bad-key / low-credits / concurrency errors as HTTP 200 with
//     top-level `success:"0"` and a `debounce.error` string (the OpenAPI portal labels these
//     401/402/429, but the live API uses 200). Decode branches on `success` and classifies
//     `debounce.error` via classifyErrMsg.
//
// VERIFIED from docs: endpoint, `api` query auth, success/error body shape, result codes 1-8.
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func DeBounce(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.debounce.io/v1/"
	}
	return &provider.HTTPAdapter{
		NameV:   "debounce",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api",
			KeyPoolSelector: "debounce:default",
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
				Success  string `json:"success"`
				DeBounce struct {
					Result string `json:"result"`
					Error  string `json:"error"`
				} `json:"debounce"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			if p.Success != "1" && p.DeBounce.Error != "" {
				return provider.Result{}, domain.NewProviderError("debounce", classifyErrMsg(p.DeBounce.Error), errors.New(p.DeBounce.Error))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.DeBounce.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.DeBounce.Result, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
