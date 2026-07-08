package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// AbstractEmail builds an adapter for the Abstract Email Validation API (docs/03 §9). Companion to
// AbstractPhone/AbstractCompany (each Abstract product has its own host + key).
//   - Endpoint: GET {base}?email=  (base default https://emailvalidation.abstractapi.com/v1/)
//     [docs.abstractapi.com/api/email-validation].
//   - Auth: API key in the "api_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (deliverability: DELIVERABLE|UNDELIVERABLE|UNKNOWN).
//   - Quirk: the boolean sub-checks (is_valid_format, is_smtp_valid, …) are nested {value,text}
//     objects, not plain booleans (not mapped — no canonical target). 422 = out of credits (QUOTA;
//     shared map treats 422 as BAD_REQUEST — documented discrepancy), 429 = req/s limit.
//
// VERIFIED from docs: endpoint, api_key query auth, deliverability enum, error codes. Exact field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func AbstractEmail(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://emailvalidation.abstractapi.com/v1/"
	}
	return &provider.HTTPAdapter{
		NameV:   "abstract-email",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api_key",
			KeyPoolSelector: "abstract-email:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
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
				Deliverability string `json:"deliverability"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Deliverability != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Deliverability, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
