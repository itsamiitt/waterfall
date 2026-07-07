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

// Scrubby builds an adapter for the Scrubby single email verification API (docs/03 §7),
// catch-all/risky specialist.
//   - Endpoint: POST {base}/validate_email with {"email":…}  (base default https://api.scrubby.io)
//     [docs.scrubby.io/scrubby-openapi.json].
//   - Auth: API key in the "x-api-key" header, injected at egress.
//   - Input: work_email. Fills email_status from `result` (Valid/Invalid/Risky/Unknown, lowercased).
//   - Quirk: 400 = invalid format OR insufficient credits (no 402); 408 = internal timeout (refunded).
//
// VERIFIED from the OpenAPI spec: base, x-api-key auth, /validate_email, `result` enum. Field names
// pinned UNVERIFIED until a live authorized call (see hunter.go).
func Scrubby(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.scrubby.io"
	}
	return &provider.HTTPAdapter{
		NameV:   "scrubby",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "x-api-key", KeyPoolSelector: "scrubby:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			b, err := json.Marshal(map[string]string{"email": req.Known[domain.FieldWorkEmail]})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/validate_email", bytes.NewReader(b))
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
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: strings.ToLower(p.Result), Confidence: 0.90}
			}
			return res, nil
		},
	}
}
