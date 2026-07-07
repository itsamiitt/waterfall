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

// MailboxValidator builds an adapter for the MailboxValidator v2 single validation API (docs/03 §9).
//   - Endpoint: GET {base}?email=  (base default https://api.mailboxvalidator.com/v2/validation/single)
//     [mailboxvalidator.com/api-single-validation].
//   - Auth: API key in the "key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (from the boolean "status" verdict → valid|invalid),
//     work_email (the echoed/validated email_address) + company_domain (parsed domain).
//   - Quirk: errors are carried in a nested "error" object {error_code,error_message}; codes map by
//     number — 10004 insufficient credits → QUOTA (returned as HTTP 401, not 402), 10000 missing
//     param → BAD_REQUEST, 10005 unknown → PROVIDER_DOWN, 10001/2/3 key not found/disabled/expired
//     → AUTH. Always inspect the error object (present even on some 200s). Booleans are JSON true/false.
//
// VERIFIED from docs: endpoint, key query auth, error_code table, the boolean status verdict. Exact
// success field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func MailboxValidator(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.mailboxvalidator.com/v2/validation/single"
	}
	return &provider.HTTPAdapter{
		NameV:   "mailboxvalidator",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "key",
			KeyPoolSelector: "mailboxvalidator:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.85},
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
				EmailAddress string `json:"email_address"`
				Domain       string `json:"domain"`
				Status       bool   `json:"status"`
				Err          struct {
					Code string `json:"error_code"`
					Msg  string `json:"error_message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			if p.Err.Code != "" {
				var class domain.ErrorClass
				switch p.Err.Code {
				case "10004":
					class = domain.ClassQuota
				case "10000":
					class = domain.ClassBadRequest
				case "10005":
					class = domain.ClassProviderDown
				default: // 10001/10002/10003 and any unlisted → auth
					class = domain.ClassAuth
				}
				msg := p.Err.Msg
				if msg == "" {
					msg = "error " + p.Err.Code
				}
				return provider.Result{}, domain.NewProviderError("mailboxvalidator", class, errors.New(msg))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			verdict := "invalid"
			if p.Status {
				verdict = "valid"
			}
			res.Values[domain.FieldEmailStatus] = provider.Observation{Value: verdict, Confidence: 0.90}
			if p.EmailAddress != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.EmailAddress, Confidence: 0.90}
			}
			if p.Domain != "" {
				res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: p.Domain, Confidence: 0.85}
			}
			return res, nil
		},
	}
}
