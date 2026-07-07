package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Bouncify builds an adapter for the Bouncify single-email validation API (docs/03 §9).
//   - Endpoint: GET {base}?email=  (base default https://api.bouncify.io/v1/verify)
//     [bouncify.readme.io/reference/single-validation-api].
//   - Auth: API key in the "apikey" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (result: deliverable|undeliverable|unknown|accept_all)
//   - work_email (the normalized echoed address).
//   - Quirks: real HTTP codes — 400 invalid email (BAD_REQUEST), 401 invalid key (AUTH), 402
//     insufficient credits (QUOTA), 429 rate limit (RATE_LIMIT); all handled by the shared status
//     map. No documented 200-with-error-body. Remaining flags (accept_all/role/free_email/disposable/
//     spamtrap) are integer 0/1/2 signals with no canonical target and are not mapped.
//
// VERIFIED from docs: endpoint, apikey query auth, result vocabulary, HTTP error codes. Exact JSON
// field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Bouncify(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.bouncify.io/v1/verify"
	}
	return &provider.HTTPAdapter{
		NameV:   "bouncify",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "apikey",
			KeyPoolSelector: "bouncify:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.85},
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
				Email  string `json:"email"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			if p.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Email, Confidence: 0.85}
			}
			return res, nil
		},
	}
}
