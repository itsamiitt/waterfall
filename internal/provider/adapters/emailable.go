package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Emailable builds an adapter for the Emailable single-email verification API (docs/03 §7).
//   - Endpoint: GET {base}?email=  (base default https://api.emailable.com/v1/verify)
//     [emailable.com/docs/api].
//   - Auth: API key in the "api_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (deliverable|undeliverable|risky|unknown) + appended
//     first/last/full name enrichment when present.
//   - Deviations (documented, not special-cased here): Emailable uses HTTP 403 for auth failure
//     (default maps 403->RATE_LIMIT) and a custom 249 "Try Again" code; both are noted UNVERIFIED.
//     It does NOT use the 200-with-error-body pattern.
//
// VERIFIED from docs: endpoint, api_key query auth, `state` verdict + `score`. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func Emailable(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.emailable.com/v1/verify"
	}
	return &provider.HTTPAdapter{
		NameV:   "emailable",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api_key",
			KeyPoolSelector: "emailable:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldFirstName, Cost: 2, ExpectedConfidence: 0.60},
			{Field: domain.FieldLastName, Cost: 2, ExpectedConfidence: 0.60},
			{Field: domain.FieldFullName, Cost: 2, ExpectedConfidence: 0.60},
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
				State     string `json:"state"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				FullName  string `json:"full_name"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.State != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.State, Confidence: 0.90}
			}
			if p.FirstName != "" {
				res.Values[domain.FieldFirstName] = provider.Observation{Value: p.FirstName, Confidence: 0.60}
			}
			if p.LastName != "" {
				res.Values[domain.FieldLastName] = provider.Observation{Value: p.LastName, Confidence: 0.60}
			}
			if p.FullName != "" {
				res.Values[domain.FieldFullName] = provider.Observation{Value: p.FullName, Confidence: 0.60}
			}
			return res, nil
		},
	}
}
