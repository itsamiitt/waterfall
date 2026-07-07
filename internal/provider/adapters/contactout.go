package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// ContactOut builds an adapter for the ContactOut people-by-LinkedIn API (docs/03 §7).
//   - Endpoint: GET {base}?profile=&include_phone=true&email_type=personal,work  (base default
//     https://api.contactout.com/v1/people/linkedin) [api.contactout.com].
//   - Auth: API key in the header literally named "token", injected at egress (AuthAPIKeyHeader).
//   - Input: linkedin_url (profile). Fills: work_email/personal_email (arrays[0]), email_status
//     (from the per-address work_email_status map), mobile_phone (phone[0]), linkedin_url.
//   - Status: DEPRIORITIZED (ADR-0009) — LinkedIn/public-web contact provenance.
//
// VERIFIED from docs: endpoint, "token" header auth, query params, {status_code, profile:{url,
// work_email[], personal_email[], work_email_status{}, phone[]}} response shape. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func ContactOut(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.contactout.com/v1/people/linkedin"
	}
	return &provider.HTTPAdapter{
		NameV:   "contactout",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "token",
			KeyPoolSelector: "contactout:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 8, ExpectedConfidence: 0.78},
			{Field: domain.FieldPersonalEmail, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldEmailStatus, Cost: 8, ExpectedConfidence: 0.72},
			{Field: domain.FieldMobilePhone, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldLinkedInURL, Cost: 8, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("profile", req.Known[domain.FieldLinkedInURL])
			q.Set("include_phone", "true")
			q.Set("email_type", "personal,work")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Profile struct {
					URL             string            `json:"url"`
					WorkEmail       []string          `json:"work_email"`
					PersonalEmail   []string          `json:"personal_email"`
					WorkEmailStatus map[string]string `json:"work_email_status"`
					Phone           []string          `json:"phone"`
				} `json:"profile"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			pr := p.Profile
			put(domain.FieldLinkedInURL, pr.URL, 0.90)
			if len(pr.WorkEmail) > 0 {
				put(domain.FieldWorkEmail, pr.WorkEmail[0], 0.78)
				if st := pr.WorkEmailStatus[pr.WorkEmail[0]]; st != "" {
					put(domain.FieldEmailStatus, st, 0.72)
				}
			}
			if len(pr.PersonalEmail) > 0 {
				put(domain.FieldPersonalEmail, pr.PersonalEmail[0], 0.75)
			}
			if len(pr.Phone) > 0 {
				put(domain.FieldMobilePhone, pr.Phone[0], 0.80)
			}
			return res, nil
		},
	}
}
