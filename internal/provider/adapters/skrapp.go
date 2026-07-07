package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Skrapp builds an adapter for the Skrapp v2 Email Finder (docs/03 §7).
//   - Endpoint: GET {base}/api/v2/find?firstName=&lastName=&fullName=&company=&domain=
//     (base default https://api.skrapp.io) [skrapp.io/api].
//   - Auth: API key in the "X-Access-Key" header, injected at egress.
//   - Input: first+last / full_name AND company_name / company_domain. Fills work_email +
//     email_status (quality.status) + echoed identity/company.
//
// VERIFIED from docs: base, X-Access-Key auth, /api/v2/find endpoint + inputs, response email/
// quality.status/firstName/lastName/name/company/domain. Field names pinned UNVERIFIED (hunter.go).
func Skrapp(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.skrapp.io"
	}
	return &provider.HTTPAdapter{
		NameV:   "skrapp",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-Access-Key", KeyPoolSelector: "skrapp:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.82},
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.78},
			{Field: domain.FieldFirstName, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldLastName, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldFullName, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/api/v2/find")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "firstName", req.Known[domain.FieldFirstName])
			setIf(q, "lastName", req.Known[domain.FieldLastName])
			setIf(q, "fullName", req.Known[domain.FieldFullName])
			setIf(q, "company", req.Known[domain.FieldCompanyName])
			setIf(q, "domain", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email   string `json:"email"`
				Quality struct {
					Status string `json:"status"`
				} `json:"quality"`
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
				Name      string `json:"name"`
				Company   string `json:"company"`
				Domain    string `json:"domain"`
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
			put(domain.FieldWorkEmail, p.Email, 0.82)
			put(domain.FieldEmailStatus, p.Quality.Status, 0.78)
			put(domain.FieldFirstName, p.FirstName, 0.85)
			put(domain.FieldLastName, p.LastName, 0.85)
			put(domain.FieldFullName, p.Name, 0.85)
			put(domain.FieldCompanyName, p.Company, 0.70)
			put(domain.FieldCompanyDomain, p.Domain, 0.75)
			return res, nil
		},
	}
}
