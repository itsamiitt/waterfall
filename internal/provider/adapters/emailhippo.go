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

// hippoKeyPlaceholder is the letters-only path sentinel Email Hippo's Build writes where the license
// key belongs; the egress AuthInjector (AuthAPIKeyPath, ADR-0024 Phase 4) substitutes the leased key.
const hippoKeyPlaceholder = "EMAILHIPPOAPIKEY"

// EmailHippo builds an adapter for the Email Hippo MORE (v3) verification API (docs/03 §9).
//   - Endpoint: GET {base}/v3/more/json/{licenseKey}/{email}  (base default https://api.hippoapi.com);
//     BOTH the key and the email are URL path segments [email-verify-api-docs.readthedocs.io].
//   - Auth: the license key is a URL PATH SEGMENT — AuthAPIKeyPath: Build writes the EMAILHIPPOAPIKEY
//     sentinel, the egress injector replaces it with the leased key. Adapter holds no secret.
//   - Input: work_email. Fills: email_status (emailVerification.mailboxVerification.result:
//     Ok|Bad|RetryLater|Unverifiable) + company_domain (meta.domain, parsed from the input).
//   - Quirk: 401 conflates bad/expired key AND exhausted quota; RetryLater/Unverifiable arrive as
//     HTTP 200 verdicts in the body (soft outcomes, not errors). Some schema views render result as
//     a numeric code — Decode reads it defensively as raw JSON and trims quotes.
//
// VERIFIED from docs: path-key endpoint, result enum, meta.* fields, 401 conflation. Exact field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func EmailHippo(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.hippoapi.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "emailhippo",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyPath,
			PathPlaceholder: hippoKeyPlaceholder,
			KeyPoolSelector: "emailhippo:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			email := url.PathEscape(req.Known[domain.FieldWorkEmail])
			u := strings.TrimRight(base, "/") + "/v3/more/json/" + hippoKeyPlaceholder + "/" + email
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Meta struct {
					Domain string `json:"domain"`
				} `json:"meta"`
				EmailVerification struct {
					MailboxVerification struct {
						Result json.RawMessage `json:"result"`
					} `json:"mailboxVerification"`
				} `json:"emailVerification"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			// result may be a string enum or (in some serializations) a numeric code — read defensively.
			if v := rawStr(p.EmailVerification.MailboxVerification.Result); v != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: v, Confidence: 0.90}
			}
			if p.Meta.Domain != "" {
				res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: p.Meta.Domain, Confidence: 0.65}
			}
			return res, nil
		},
	}
}
