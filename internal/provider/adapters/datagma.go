package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Datagma builds an adapter for the Datagma findEmail v8 API (docs/03 §7).
//   - Endpoint: GET {base}?fullName=&company=  (base default
//     https://gateway.datagma.net/api/ingress/v8/findEmail) [datagmaapi.readme.io].
//   - Auth: API key in the "apiId" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: full_name / first+last, company_name or company_domain. Fills: work_email (MX/SMTP
//     pre-verified), email_status (`status`), company_domain (`emailDomain`).
//   - No match = 200 with an empty/absent email (a missing email yields no work_email). The exact
//     in-body error shape for bad key / out-of-credits is UNVERIFIED, so it is not special-cased.
//
// VERIFIED from the ReadMe OpenAPI export: endpoint, apiId query auth, response fields
// email/emailDomain/status/mxfound/smtpCheck. Field names pinned UNVERIFIED until a live call.
func Datagma(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://gateway.datagma.net/api/ingress/v8/findEmail"
	}
	return &provider.HTTPAdapter{
		NameV:   "datagma",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "apiId",
			KeyPoolSelector: "datagma:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 9, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmailStatus, Cost: 9, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 9, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "fullName", req.Known[domain.FieldFullName])
			setIf(q, "firstName", req.Known[domain.FieldFirstName])
			setIf(q, "lastName", req.Known[domain.FieldLastName])
			company := req.Known[domain.FieldCompanyDomain]
			if company == "" {
				company = req.Known[domain.FieldCompanyName]
			}
			setIf(q, "company", company)
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email       string `json:"email"`
				EmailDomain string `json:"emailDomain"`
				Status      string `json:"status"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Email, Confidence: 0.80}
			}
			if p.Status != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Status, Confidence: 0.90}
			}
			if p.EmailDomain != "" {
				res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: p.EmailDomain, Confidence: 0.70}
			}
			return res, nil
		},
	}
}
