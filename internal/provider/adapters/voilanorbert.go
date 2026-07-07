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

// VoilaNorbert builds an adapter for the Voila Norbert email-finder API (docs/03 §2).
//   - Endpoint: POST {base} with form fields name, domain  (base default
//     https://api.voilanorbert.com/2019-01-04/search/name) [voilanorbert.com/api/].
//   - Auth: HTTP Basic, injected at egress (AuthBasic). Norbert's username can be ANY non-empty
//     string and the password is the API token, so the "<slug>:default" key pool must hold the full
//     Basic credential "any:<API_TOKEN>" (egress base64-encodes the pool secret verbatim).
//   - Input: full_name (+ company_domain). Fills: work_email (email.email) + email_status (the
//     numeric 0-100 email.score, stringified — the closest canonical email-quality signal).
//   - Quirk: async happy-path returns is_done=true synchronously; when is_done=false the result is
//     delivered to an optional webhook and there is NO documented GET/poll endpoint, so a pending
//     lookup yields no value here (NOT_FOUND) rather than blocking. HTTP error codes are UNVERIFIED.
//
// VERIFIED from docs: endpoint, form fields, Basic auth (any:token), email.{email,is_done,score}.
// Exact error-code behavior pinned UNVERIFIED until a live authorized call (see hunter.go).
func VoilaNorbert(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.voilanorbert.com/2019-01-04/search/name"
	}
	return &provider.HTTPAdapter{
		NameV:   "voila-norbert",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBasic,
			KeyPoolSelector: "voila-norbert:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			form := url.Values{}
			form.Set("name", req.Known[domain.FieldFullName])
			if d := req.Known[domain.FieldCompanyDomain]; d != "" {
				form.Set("domain", d)
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email struct {
					Email  string `json:"email"`
					IsDone bool   `json:"is_done"`
					Score  *int   `json:"score"`
				} `json:"email"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Email.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Email.Email, Confidence: 0.85}
			}
			if p.Email.Score != nil {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: itoa(int64(*p.Email.Score)), Confidence: 0.90}
			}
			return res, nil
		},
	}
}
