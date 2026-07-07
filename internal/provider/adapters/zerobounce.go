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

// ZeroBounce builds an adapter for the ZeroBounce v2 single-email validation API (docs/03 §7).
//   - Endpoint: GET {base}?email=  (base default https://api.zerobounce.net/v2/validate)
//     [zerobounce.net/docs/email-validation-api-quickstart/v2-validate-emails].
//   - Auth: API key in the "api_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (valid|invalid|catch-all|unknown|spamtrap|abuse|
//     do_not_mail) + appended first_name/last_name when present.
//   - Quirk: on a bad key OR exhausted credits ZeroBounce returns HTTP 200 with a body
//     `{"error":"Invalid API Key or your account ran out of credits"}` (the message conflates
//     both). Decode maps that to ClassAuth so the engine disables/alerts the key rather than
//     treating it as a value. A non-existent mailbox is NOT an error: HTTP 200 with status=invalid.
//
// VERIFIED from docs: endpoint, api_key query auth, status vocabulary, the 200-with-error body.
// Exact response field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func ZeroBounce(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.zerobounce.net/v2/validate"
	}
	return &provider.HTTPAdapter{
		NameV:   "zerobounce",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api_key",
			KeyPoolSelector: "zerobounce:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldFirstName, Cost: 2, ExpectedConfidence: 0.70},
			{Field: domain.FieldLastName, Cost: 2, ExpectedConfidence: 0.70},
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
				Error     string `json:"error"`
				Status    string `json:"status"`
				FirstName string `json:"firstname"`
				LastName  string `json:"lastname"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body: bad key / out of credits. Classify as AUTH (non-retryable; the
			// message conflates key + credits, so alert/disable is the safe action).
			if p.Error != "" {
				return provider.Result{}, domain.NewProviderError("zerobounce", domain.ClassAuth, errors.New(p.Error))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Status != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Status, Confidence: 0.90}
			}
			if p.FirstName != "" {
				res.Values[domain.FieldFirstName] = provider.Observation{Value: p.FirstName, Confidence: 0.70}
			}
			if p.LastName != "" {
				res.Values[domain.FieldLastName] = provider.Observation{Value: p.LastName, Confidence: 0.70}
			}
			return res, nil
		},
	}
}
