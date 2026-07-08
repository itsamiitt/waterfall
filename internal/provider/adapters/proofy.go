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

// Proofy builds an adapter for the Proofy single email verification API (docs/03 §9).
//   - Endpoint: GET {base}/verify/single?email=  (base default https://apis.proofy.io/v1)
//     [docs.proofy.io/api-reference/endpoint/verify-single].
//   - Auth: API key in the "api_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (status: valid|invalid|risky|unknown).
//   - Quirk: 400 = "Not enough credits" (QUOTA, not bad-request — the shared map treats 400 as
//     BAD_REQUEST; documented discrepancy). The vendor's OpenAPI is internally inconsistent about
//     whether status is at $.status (flat, per the example) or $.result.status (per the schema), so
//     Decode reads BOTH and prefers whichever is populated.
//
// VERIFIED from the vendor OpenAPI spec: endpoint, api_key query auth, status enum, error codes. The
// flat-vs-nested status path is pinned defensively UNVERIFIED until a live key (see hunter.go).
func Proofy(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://apis.proofy.io/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "proofy",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api_key",
			KeyPoolSelector: "proofy:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/verify/single")
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
				Status string `json:"status"`
				Result struct {
					Status string `json:"status"`
				} `json:"result"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			status := p.Status
			if status == "" {
				status = p.Result.Status
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if status != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: status, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
