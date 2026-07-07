package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Findymail builds an adapter for the Findymail find-by-name email finder (docs/03 §7).
//   - Endpoint: POST {base} with JSON {name, domain}  (base default
//     https://app.findymail.com/api/search/name) [app.findymail.com/docs].
//   - Auth: Bearer token, injected at egress (AuthBearer).
//   - Input: full_name (or first_name+last_name) + company_domain. Fills: work_email (verified-only
//     finder, ~<5% bounce), plus the echoed full_name / company_domain.
//   - Quirks: bad key -> 401 (AUTH), out-of-credits -> 402 (QUOTA), paused subscription -> 423
//     (now mapped to QUOTA in classifyStatus). No 200-with-error-body. No match = 200 with
//     contact=null / no email -> a missing contact.email yields no work_email (NOT_FOUND).
//
// VERIFIED from docs: endpoint, Bearer auth, {name,domain} request, {contact:{name,domain,email}}
// success shape, error codes. Field names pinned UNVERIFIED until a live authorized call.
func Findymail(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://app.findymail.com/api/search/name"
	}
	return &provider.HTTPAdapter{
		NameV:   "findymail",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "findymail:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldFullName, Cost: 8, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyDomain, Cost: 8, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			name := req.Known[domain.FieldFullName]
			if name == "" {
				name = strings.TrimSpace(req.Known[domain.FieldFirstName] + " " + req.Known[domain.FieldLastName])
			}
			payload := map[string]string{"name": name, "domain": req.Known[domain.FieldCompanyDomain]}
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Contact struct {
					Name   string `json:"name"`
					Domain string `json:"domain"`
					Email  string `json:"email"`
				} `json:"contact"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Contact.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Contact.Email, Confidence: 0.80}
			}
			if p.Contact.Name != "" {
				res.Values[domain.FieldFullName] = provider.Observation{Value: p.Contact.Name, Confidence: 0.70}
			}
			if p.Contact.Domain != "" {
				res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: p.Contact.Domain, Confidence: 0.70}
			}
			return res, nil
		},
	}
}
