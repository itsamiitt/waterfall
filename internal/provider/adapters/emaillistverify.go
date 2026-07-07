package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// EmailListVerify builds an adapter for the EmailListVerify detailed single-email API (docs/03 §9).
//   - Endpoint: GET {base}?email=  (base default https://api.emaillistverify.com/api/verifyEmailDetailed)
//     [api.emaillistverify.com/api-doc OpenAPI 3.0]. The sibling /api/verifyEmail returns a bare
//     PLAIN-TEXT status; this adapter uses the *Detailed* endpoint, which returns JSON.
//   - Auth: API key in the "x-api-key" HEADER, injected at egress (AuthAPIKeyHeader).
//   - Input: work_email. Fills: email_status (result enum ok|unknown|dead_server|invalid_mx|
//     email_disabled|antispam_system|ok_for_all|smtp_protocol|invalid_syntax|disposable|spamtrap)
//   - first_name/last_name (provider "estimations" from the local part, nullable, modeled ~0.65).
//   - Quirk: rate limiting surfaces as HTTP 400 ("Too many requests…") — the shared map classifies
//     400 as BAD_REQUEST, not RATE_LIMIT; credit exhaustion is HTTP 403 (shared map → RATE_LIMIT)
//     while 403 also carries "account disabled". These discrepancies are documented, not overridden.
//
// VERIFIED from the vendor OpenAPI spec: endpoint, x-api-key header, result enum, error codes. Exact
// JSON field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func EmailListVerify(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.emaillistverify.com/api/verifyEmailDetailed"
	}
	return &provider.HTTPAdapter{
		NameV:   "emaillistverify",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "x-api-key",
			KeyPoolSelector: "emaillistverify:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldFirstName, Cost: 1, ExpectedConfidence: 0.65},
			{Field: domain.FieldLastName, Cost: 1, ExpectedConfidence: 0.65},
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
				Result    string `json:"result"`
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			if p.FirstName != "" {
				res.Values[domain.FieldFirstName] = provider.Observation{Value: p.FirstName, Confidence: 0.65}
			}
			if p.LastName != "" {
				res.Values[domain.FieldLastName] = provider.Observation{Value: p.LastName, Confidence: 0.65}
			}
			return res, nil
		},
	}
}
