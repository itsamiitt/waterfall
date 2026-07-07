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

// UpLead builds an adapter for the UpLead v2 Person Search API (docs/03 §7).
//   - Endpoint: POST {base}person-search with {email | first_name+last_name+domain}  (base default
//     https://api.uplead.com/v2/) [docs.uplead.com].
//   - Auth: raw API key in the "Authorization" header (NO "Bearer" prefix), injected at egress.
//   - Input: work_email OR first+last+company_domain. Fills contact + basic firmo. Charged only when
//     a valid contact is returned.
//
// VERIFIED from docs: base, raw-Authorization auth, /person-search inputs, response email/email_status/
// mobile_directdial/linkedin_url/title/management_level/company_name/domain/industry/first_name/
// last_name (+ employees/revenue on the combined/company endpoints). Field names UNVERIFIED (hunter.go).
func UpLead(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.uplead.com/v2/"
	}
	return &provider.HTTPAdapter{
		NameV:   "uplead",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "Authorization", KeyPoolSelector: "uplead:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 4, ExpectedConfidence: 0.82},
			{Field: domain.FieldEmailStatus, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldMobilePhone, Cost: 4, ExpectedConfidence: 0.72},
			{Field: domain.FieldLinkedInURL, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldJobTitle, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldSeniority, Cost: 4, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyName, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldIndustry, Cost: 4, ExpectedConfidence: 0.75},
			{Field: domain.FieldEmployeeCount, Cost: 4, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyRevenue, Cost: 4, ExpectedConfidence: 0.70},
			{Field: domain.FieldFirstName, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldLastName, Cost: 4, ExpectedConfidence: 0.85},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			m := map[string]string{}
			putIf(m, "email", req.Known[domain.FieldWorkEmail])
			putIf(m, "first_name", req.Known[domain.FieldFirstName])
			putIf(m, "last_name", req.Known[domain.FieldLastName])
			putIf(m, "domain", req.Known[domain.FieldCompanyDomain])
			b, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/person-search", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email            string          `json:"email"`
				EmailStatus      string          `json:"email_status"`
				MobileDirectDial string          `json:"mobile_directdial"`
				LinkedInURL      string          `json:"linkedin_url"`
				Title            string          `json:"title"`
				ManagementLevel  string          `json:"management_level"`
				CompanyName      string          `json:"company_name"`
				Domain           string          `json:"domain"`
				Industry         string          `json:"industry"`
				Employees        json.RawMessage `json:"employees"`
				Revenue          json.RawMessage `json:"revenue"`
				FirstName        string          `json:"first_name"`
				LastName         string          `json:"last_name"`
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
			put(domain.FieldEmailStatus, p.EmailStatus, 0.80)
			put(domain.FieldMobilePhone, p.MobileDirectDial, 0.72)
			put(domain.FieldLinkedInURL, p.LinkedInURL, 0.80)
			put(domain.FieldJobTitle, p.Title, 0.80)
			put(domain.FieldSeniority, p.ManagementLevel, 0.75)
			put(domain.FieldCompanyName, p.CompanyName, 0.80)
			put(domain.FieldCompanyDomain, p.Domain, 0.80)
			put(domain.FieldIndustry, p.Industry, 0.75)
			put(domain.FieldEmployeeCount, rawStr(p.Employees), 0.70)
			put(domain.FieldCompanyRevenue, rawStr(p.Revenue), 0.70)
			put(domain.FieldFirstName, p.FirstName, 0.85)
			put(domain.FieldLastName, p.LastName, 0.85)
			return res, nil
		},
	}
}
