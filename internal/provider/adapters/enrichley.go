package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Enrichley builds an adapter for the Enrichley v1 single email validation API (docs/03 §7),
// catch-all specialist.
//   - Endpoint: POST {base}/validate-single-email with {"email":…}  (base default
//     https://api.enrichley.io/api/v1) [docs.enrichley.io].
//   - Auth: API key in the "X-Api-Key" header, injected at egress.
//   - Input: work_email. Fills email_status from `result`
//     (ok/catch_all_validated/catch_all/invalid/unknown).
//
// VERIFIED from the OpenAPI spec: base, X-Api-Key auth, /validate-single-email, `result` enum +
// example body. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Enrichley(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.enrichley.io/api/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "enrichley",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-Api-Key", KeyPoolSelector: "enrichley:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			b, err := json.Marshal(map[string]string{"email": req.Known[domain.FieldWorkEmail]})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/validate-single-email", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
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
