package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Mailboxlayer builds an adapter for the APILayer mailboxlayer email verification API (docs/03 §9).
//   - Endpoint: GET {base}/check?email=  (base default https://apilayer.net/api — the legacy-style
//     apilayer.net product, distinct from the api.apilayer.com marketplace variant)
//     [docs.apilayer.com/mailboxlayer, error behavior live-verified].
//   - Auth: API key in the "access_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (boolean smtp_check → valid|invalid), work_email /
//     personal_email (the echoed address, classified by the free/disposable flags — modeled ~0.6),
//     company_domain (domain, only when free=false && disposable=false).
//   - Quirk: ALL errors are HTTP 200 with {success:false,error:{code,type,info}} (verified live) —
//     Decode classifies by numeric code: 101/102 auth, 104 quota, 106 rate-limit, 999 transient,
//     else bad-request. Free plan: 250 req/month, 50 req/min.
//
// VERIFIED from docs + live unauthenticated probes: endpoint, access_key auth, response fields,
// error codes. Success field names pinned UNVERIFIED until a live key (see hunter.go).
func Mailboxlayer(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://apilayer.net/api"
	}
	return &provider.HTTPAdapter{
		NameV:   "mailboxlayer",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "access_key",
			KeyPoolSelector: "mailboxlayer:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.60},
			{Field: domain.FieldPersonalEmail, Cost: 1, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.55},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/check")
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
				Success   *bool `json:"success"`
				ErrorInfo struct {
					Code int    `json:"code"`
					Type string `json:"type"`
					Info string `json:"info"`
				} `json:"error"`
				Email      string `json:"email"`
				Domain     string `json:"domain"`
				SMTPCheck  *bool  `json:"smtp_check"`
				Free       bool   `json:"free"`
				Disposable bool   `json:"disposable"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body (the ONLY error channel): classify by numeric code.
			if p.Success != nil && !*p.Success {
				var class domain.ErrorClass
				switch p.ErrorInfo.Code {
				case 101, 102:
					class = domain.ClassAuth
				case 104:
					class = domain.ClassQuota
				case 106:
					class = domain.ClassRateLimit
				case 999:
					class = domain.ClassTransient
				default:
					class = domain.ClassBadRequest
				}
				msg := p.ErrorInfo.Info
				if msg == "" {
					msg = p.ErrorInfo.Type
				}
				return provider.Result{}, domain.NewProviderError("mailboxlayer", class, errors.New(msg))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.SMTPCheck != nil {
				verdict := "invalid"
				if *p.SMTPCheck {
					verdict = "valid"
				}
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: verdict, Confidence: 0.85}
			}
			if p.Email != "" {
				if p.Free {
					res.Values[domain.FieldPersonalEmail] = provider.Observation{Value: p.Email, Confidence: 0.60}
				} else if !p.Disposable {
					res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Email, Confidence: 0.60}
				}
			}
			if p.Domain != "" && !p.Free && !p.Disposable {
				res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: p.Domain, Confidence: 0.55}
			}
			return res, nil
		},
	}
}
