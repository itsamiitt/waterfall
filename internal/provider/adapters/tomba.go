package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Tomba builds an adapter for the Tomba Email Finder API (docs/03 §7).
//   - Endpoint: GET {base}/email-finder?domain=&full_name=&first_name=&last_name=&company=
//     (base default https://api.tomba.io/v1) [docs.tomba.io/api/finder].
//   - Auth: TWO required headers — X-Tomba-Key (ta_…) + X-Tomba-Secret (ts_…), via
//     AuthAPIKeyDualHeader; the pool secret is "ta_key:ts_secret", split at egress.
//   - Input: company_domain + full_name / first+last / company_name. Fills contact + email_status.
//
// VERIFIED from docs: endpoint, dual-header auth, data.{email,verification.status,first_name,
// last_name,full_name,company,website_url,linkedin}. `position` is an object (shape unverified) so
// job_title is not mapped. Field names pinned UNVERIFIED until a live authorized call (hunter.go).
func Tomba(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.tomba.io/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "tomba",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:           provider.AuthAPIKeyDualHeader,
			HeaderName:       "X-Tomba-Key",
			SecondHeaderName: "X-Tomba-Secret",
			KeyPoolSelector:  "tomba:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldFirstName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldLastName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldFullName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldLinkedInURL, Cost: 3, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/email-finder")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "domain", req.Known[domain.FieldCompanyDomain])
			setIf(q, "full_name", req.Known[domain.FieldFullName])
			setIf(q, "first_name", req.Known[domain.FieldFirstName])
			setIf(q, "last_name", req.Known[domain.FieldLastName])
			setIf(q, "company", req.Known[domain.FieldCompanyName])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					Email        string `json:"email"`
					Verification struct {
						Status string `json:"status"`
					} `json:"verification"`
					FirstName  string `json:"first_name"`
					LastName   string `json:"last_name"`
					FullName   string `json:"full_name"`
					Company    string `json:"company"`
					WebsiteURL string `json:"website_url"`
					LinkedIn   string `json:"linkedin"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			d := p.Data
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, d.Email, 0.85)
			put(domain.FieldEmailStatus, d.Verification.Status, 0.85)
			put(domain.FieldFirstName, d.FirstName, 0.90)
			put(domain.FieldLastName, d.LastName, 0.90)
			put(domain.FieldFullName, d.FullName, 0.90)
			put(domain.FieldCompanyName, d.Company, 0.75)
			put(domain.FieldCompanyDomain, bareDomain(d.WebsiteURL), 0.80)
			put(domain.FieldLinkedInURL, d.LinkedIn, 0.70)
			return res, nil
		},
	}
}
