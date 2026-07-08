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

// Kendo builds an adapter for the Kendo Email Finder API (docs/03 §2).
//   - Endpoint: GET {base}/emailbyname?name=&domain=  (base default https://kendoemailapp.com)
//     [kendoemailapp.com/swagger, live-verified error behavior].
//   - Auth: API key in the "apikey" QUERY parameter, injected at egress (AuthAPIKeyQuery). (The
//     marketing page's "bearer token" wording is inaccurate — the Swagger spec + live probe confirm
//     query-param auth.)
//   - Input: full_name + company_domain (domain is required). Fills work_email + personal_email
//     (the vendor's private_email field).
//   - Quirk: errors carry NO JSON body — only the HTTP status line. 403 is reused for BOTH auth AND
//     missing-param failures; 405 = out of credit (QUOTA; the shared map has no 405 case → Transient
//     — documented discrepancy); 404 = no match (not charged); 410 (emailbylinkedin only) = soft miss.
//
// VERIFIED from the vendor's self-hosted Swagger 2.0 + live probes: endpoint, apikey query auth,
// PersonEmail {work_email,private_email} schema. Exact field names pinned UNVERIFIED (see hunter.go).
func Kendo(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://kendoemailapp.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "kendo",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "apikey",
			KeyPoolSelector: "kendo:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldPersonalEmail, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/emailbyname")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("name", req.Known[domain.FieldFullName])
			q.Set("domain", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				WorkEmail    string `json:"work_email"`
				PrivateEmail string `json:"private_email"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.WorkEmail != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.WorkEmail, Confidence: 0.90}
			}
			if p.PrivateEmail != "" {
				res.Values[domain.FieldPersonalEmail] = provider.Observation{Value: p.PrivateEmail, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
