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

// Cufinder builds an adapter for the CUFinder v2 Person Enrichment endpoint (/tep) (docs/03 §7).
//   - Endpoint: POST {base}/tep (form-urlencoded body full_name=&company=)  (base default
//     https://api.cufinder.io/v2) [github.com/CUFinder/cufinder-go; apidoc].
//   - Auth: API key in the "x-api-key" header, injected at egress.
//   - Input: full_name + company_name (both required by /tep). Fills contact + firmographics from
//     data.person. Envelope {status, data, credit_count}; status:-1 on failure (treated as no-match).
//
// VERIFIED from the official Go SDK + apidoc: base, x-api-key, /tep endpoint, data.person fields
// (email/first_name/last_name/full_name/job_title/linkedin_url/company_name/company_website/
// company_industry/company_size). email_status not offered (only a numeric confidence). Field names
// pinned UNVERIFIED until a live authorized call (see hunter.go).
func Cufinder(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.cufinder.io/v2"
	}
	return &provider.HTTPAdapter{
		NameV:   "cufinder",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "x-api-key", KeyPoolSelector: "cufinder:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldFirstName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldLastName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldFullName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldJobTitle, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldLinkedInURL, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			form := url.Values{}
			setIf(form, "full_name", req.Known[domain.FieldFullName])
			setIf(form, "company", req.Known[domain.FieldCompanyName])
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/tep", strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					Person struct {
						Email           string `json:"email"`
						FirstName       string `json:"first_name"`
						LastName        string `json:"last_name"`
						FullName        string `json:"full_name"`
						JobTitle        string `json:"job_title"`
						LinkedInURL     string `json:"linkedin_url"`
						CompanyName     string `json:"company_name"`
						CompanyWebsite  string `json:"company_website"`
						CompanyIndustry string `json:"company_industry"`
						CompanySize     string `json:"company_size"`
					} `json:"person"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			pr := p.Data.Person
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, pr.Email, 0.80)
			put(domain.FieldFirstName, pr.FirstName, 0.85)
			put(domain.FieldLastName, pr.LastName, 0.85)
			put(domain.FieldFullName, pr.FullName, 0.85)
			put(domain.FieldJobTitle, pr.JobTitle, 0.80)
			put(domain.FieldLinkedInURL, pr.LinkedInURL, 0.80)
			put(domain.FieldCompanyName, pr.CompanyName, 0.80)
			put(domain.FieldCompanyDomain, bareDomain(pr.CompanyWebsite), 0.80)
			put(domain.FieldIndustry, pr.CompanyIndustry, 0.70)
			put(domain.FieldEmployeeCount, pr.CompanySize, 0.65)
			return res, nil
		},
	}
}
