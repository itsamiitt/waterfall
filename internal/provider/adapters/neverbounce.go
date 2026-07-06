package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// NeverBounce builds an adapter for the NeverBounce single-check email verification API (docs/03).
//   - Endpoint: GET {base}?email=  (base default https://api.neverbounce.com/v4/single/check)
//     [https://developers.neverbounce.com/reference/single-check].
//   - Auth: API key in the "key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status ("valid"|"invalid"|"disposable"|"catchall"|"unknown").
//   - Quirk: NeverBounce signals auth/credit errors as HTTP 200 with a body `status` != "success"
//     (not an HTTP code). Decode maps only a present `result`; an error body yields NO email_status
//     observation (the waterfall falls through) rather than a fabricated value.
//
// VERIFIED from docs: endpoint, `key` query auth, result vocabulary. Exact JSON field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func NeverBounce(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.neverbounce.com/v4/single/check"
	}
	return &provider.HTTPAdapter{
		NameV:   "neverbounce",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "key",
			KeyPoolSelector: "neverbounce:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.90},
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
				Status string `json:"status"` // "success" | "auth_failure" | "payment_failure" | ...
				Result string `json:"result"` // "valid" | "invalid" | "disposable" | "catchall" | "unknown"
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
