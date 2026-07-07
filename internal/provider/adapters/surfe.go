package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Surfe builds an async adapter for the Surfe v2 people-enrichment API (docs/03 §2).
//   - Submit: POST {base}/v2/people/enrich with {people:[{firstName,lastName,companyName,
//     companyDomain,linkedinUrl}],include:{email,mobile}} → 202 with enrichmentID.
//   - Poll: GET {base}/v2/people/enrich/{id} ~1/s until status ∈ {COMPLETED,FAILED} (IN_PROGRESS
//     while running).
//   - Auth: static bearer "Authorization: Bearer <key>", injected at egress (AuthBearer).
//   - base default https://api.surfe.com [developers.surfe.com].
//   - Fills work_email + email_status (emails[].validationStatus, e.g. VALID), mobile_phone,
//     job_title, seniority/department (arrays→normalized), linkedin_url, first/last name, company_name/domain.
//   - Quirk: 403 is overloaded for BOTH quota AND insufficient credits (no 402); the shared map treats
//     403 as RATE_LIMIT. NOTE the response key is "linkedInUrl" (capital I) vs request "linkedinUrl".
//
// VERIFIED from docs: submit/poll endpoints, bearer auth, enrichmentID token, status vocabulary,
// per-person emails/mobilePhones. Exact JSON field names pinned UNVERIFIED until a live key (see hunter.go).
func Surfe(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.surfe.com"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "surfe",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "surfe:default"},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 1 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldJobTitle, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldSeniority, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldDepartment, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldLinkedInURL, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldFirstName, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldLastName, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyName, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.85},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			person := map[string]string{}
			putIf(person, "firstName", req.Known[domain.FieldFirstName])
			putIf(person, "lastName", req.Known[domain.FieldLastName])
			putIf(person, "companyName", req.Known[domain.FieldCompanyName])
			putIf(person, "companyDomain", req.Known[domain.FieldCompanyDomain])
			putIf(person, "linkedinUrl", req.Known[domain.FieldLinkedInURL])
			body := map[string]any{
				"people":  []any{person},
				"include": map[string]bool{"email": true, "mobile": true},
			}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v2/people/enrich", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				EnrichmentID string `json:"enrichmentID"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.EnrichmentID == "" {
				return "", domain.NewProviderError("surfe", domain.ClassTransient, errNoJobID)
			}
			return p.EnrichmentID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/people/enrich/"+url.PathEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Status string `json:"status"`
				People []struct {
					FirstName     string   `json:"firstName"`
					LastName      string   `json:"lastName"`
					CompanyName   string   `json:"companyName"`
					CompanyDomain string   `json:"companyDomain"`
					LinkedInURL   string   `json:"linkedInUrl"`
					JobTitle      string   `json:"jobTitle"`
					Seniorities   []string `json:"seniorities"`
					Departments   []string `json:"departments"`
					Emails        []struct {
						Email            string `json:"email"`
						ValidationStatus string `json:"validationStatus"`
					} `json:"emails"`
					MobilePhones []struct {
						MobilePhone string `json:"mobilePhone"`
					} `json:"mobilePhones"`
				} `json:"people"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if p.Status != "COMPLETED" && p.Status != "FAILED" {
				return provider.Result{}, false, nil // IN_PROGRESS — keep polling
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Status == "COMPLETED" && len(p.People) > 0 {
				pe := p.People[0]
				put := func(f domain.Field, v string, c domain.Confidence) {
					if v != "" {
						res.Values[f] = provider.Observation{Value: v, Confidence: c}
					}
				}
				if len(pe.Emails) > 0 {
					put(domain.FieldWorkEmail, pe.Emails[0].Email, 0.85)
					put(domain.FieldEmailStatus, pe.Emails[0].ValidationStatus, 0.90)
				}
				if len(pe.MobilePhones) > 0 {
					put(domain.FieldMobilePhone, pe.MobilePhones[0].MobilePhone, 0.85)
				}
				put(domain.FieldJobTitle, pe.JobTitle, 0.85)
				put(domain.FieldSeniority, normList(pe.Seniorities), 0.85)
				put(domain.FieldDepartment, normList(pe.Departments), 0.85)
				put(domain.FieldLinkedInURL, pe.LinkedInURL, 0.85)
				put(domain.FieldFirstName, pe.FirstName, 0.85)
				put(domain.FieldLastName, pe.LastName, 0.85)
				put(domain.FieldCompanyName, pe.CompanyName, 0.85)
				put(domain.FieldCompanyDomain, pe.CompanyDomain, 0.85)
			}
			return res, true, nil
		},
	}
}
