package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// MailgunValidate builds an adapter for the Mailgun v4 Email Validation API (docs/03 §7).
//   - Endpoint: GET {base}?address=  (base default https://api.mailgun.net/v4/address/validate)
//     [documentation.mailgun.com/docs/validate].
//   - Auth: HTTP Basic (username "api", password = Mailgun private key) — the key pool secret must
//     be stored as "api:<key>", injected at egress (AuthBasic), mirroring the Twilio pattern.
//   - Input: work_email. Fills: email_status (deliverable|undeliverable|do_not_send|catch_all|
//     unknown).
//   - Standard status codes (401 for bad key; no 200-with-error-body). EU users swap the host for
//     api.eu.mailgun.net via the base override.
//
// VERIFIED from docs: endpoint, Basic api:key auth, `result` verdict + `risk`. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func MailgunValidate(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.mailgun.net/v4/address/validate"
	}
	return &provider.HTTPAdapter{
		NameV:   "mailgun-validate",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBasic,
			KeyPoolSelector: "mailgun-validate:default", // secret stored as "api:<key>"
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("address", req.Known[domain.FieldWorkEmail])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
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
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
