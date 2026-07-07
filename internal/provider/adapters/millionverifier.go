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

// MillionVerifier builds an adapter for the MillionVerifier v3 real-time verification API (docs/03).
//   - Endpoint: GET {base}?email=  (base default https://api.millionverifier.com/api/v3/)
//     [developer.millionverifier.com].
//   - Auth: API key in the "api" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (ok|catch_all|unknown|disposable|invalid).
//   - Quirk: MillionVerifier returns EVERYTHING as HTTP 200 — including a bad/missing key,
//     insufficient credits, IP block, and internal errors — with the detail in a non-empty `error`
//     field. Decode classifies a non-empty `error` via classifyErrMsg (AUTH/QUOTA/…), which the
//     enhanced HTTPAdapter preserves.
//
// VERIFIED from docs: endpoint, `api` query auth, `result` vocabulary, the 200-with-error field.
// Exact error strings are UNVERIFIED, so we branch on `error` being non-empty (see hunter.go).
func MillionVerifier(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.millionverifier.com/api/v3/"
	}
	return &provider.HTTPAdapter{
		NameV:   "millionverifier",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api",
			KeyPoolSelector: "millionverifier:default",
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
				Result string `json:"result"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			if p.Error != "" {
				return provider.Result{}, domain.NewProviderError("millionverifier", classifyErrMsg(p.Error), errors.New(p.Error))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
