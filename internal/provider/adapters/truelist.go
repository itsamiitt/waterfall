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

// Truelist builds an adapter for the Truelist real-time verify API (docs/03 §9).
//   - Endpoint: POST {base}/api/v1/verify_inline?email=  (base default https://api.truelist.io);
//     the address is a QUERY parameter on a POST [truelist.io/docs/api].
//   - Auth: bearer token "Authorization: Bearer <key>", injected at egress (AuthBearer).
//   - Input: work_email. Fills: email_status (emails[0].email_state: ok|email_invalid|risky|
//     accept_all|unknown) + work_email (echoed address) + company_domain (parsed domain, ~0.7).
//   - Quirk: an invalid address is HTTP 200 with email_state="email_invalid" (branch on the body,
//     not the status); 429 = 10 req/s limit.
//
// VERIFIED from docs/blog examples: endpoint, bearer auth, emails[] body, email_state enum. Exact
// field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Truelist(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.truelist.io"
	}
	return &provider.HTTPAdapter{
		NameV:   "truelist",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "truelist:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/api/v1/verify_inline")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("email", req.Known[domain.FieldWorkEmail])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Emails []struct {
					Address    string `json:"address"`
					Domain     string `json:"domain"`
					EmailState string `json:"email_state"`
				} `json:"emails"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Emails) > 0 {
				e := p.Emails[0]
				if e.EmailState != "" {
					res.Values[domain.FieldEmailStatus] = provider.Observation{Value: e.EmailState, Confidence: 0.90}
				}
				if e.Address != "" {
					res.Values[domain.FieldWorkEmail] = provider.Observation{Value: e.Address, Confidence: 0.90}
				}
				if e.Domain != "" {
					res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: e.Domain, Confidence: 0.70}
				}
			}
			return res, nil
		},
	}
}
