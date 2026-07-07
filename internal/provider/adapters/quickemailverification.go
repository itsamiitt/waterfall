package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// QuickEmailVerification builds an adapter for the QuickEmailVerification single-email verify API
// (docs/03 §9).
//   - Endpoint: GET {base}?email=  (base default https://api.quickemailverification.com/v1/verify)
//     [docs.quickemailverification.com/email-verification-api/verify-an-email-address].
//   - Auth: API key in the "apikey" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (result: valid|invalid|unknown) + company_domain
//     (the domain portion parsed from the input email — may be a free/personal provider domain).
//   - Quirk: 200-with-error-body — on failure the body carries success:"false" (a STRING, not a
//     JSON bool) + a "message"; some conditions (low credit, IP-not-allowed) surface this way
//     rather than as a distinct HTTP status. Decode classifies the message (credit→QUOTA, else
//     AUTH). All boolean flags are returned as "true"/"false" strings.
//
// VERIFIED from docs: endpoint, apikey query auth, result vocabulary, the 200-with-error body and
// string-boolean quirk. Exact JSON field names pinned UNVERIFIED until a live key (see hunter.go).
func QuickEmailVerification(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.quickemailverification.com/v1/verify"
	}
	return &provider.HTTPAdapter{
		NameV:   "quickemailverification",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "apikey",
			KeyPoolSelector: "quickemailverification:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.70},
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
				Result  string `json:"result"`
				Domain  string `json:"domain"`
				Success string `json:"success"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body: success:"false" (string). Classify the message.
			if p.Success == "false" {
				return provider.Result{}, bodyErr("quickemailverification", p.Message)
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			if p.Domain != "" {
				res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: p.Domain, Confidence: 0.70}
			}
			return res, nil
		},
	}
}
