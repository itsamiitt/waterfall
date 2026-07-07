package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Wiza builds an async adapter for the Wiza Individual Reveal API (docs/03 §7).
//   - Submit: POST {base}/api/individual_reveals with {"individual_reveal":{profile_url,full_name,
//     company,domain,email},"enrichment_level":"full"} → data.id.
//   - Poll: GET {base}/api/individual_reveals/{id} until data.status in {finished,failed}
//     (queued/resolving pending; failed = errored → empty terminal).
//   - Auth: Bearer token, injected at egress.
//   - Status: DEPRIORITIZED (ADR-0009) — LinkedIn/public-profile provenance.
//   - base default https://wiza.co [docs.wiza.co].
//
// VERIFIED from docs: endpoints, Bearer, data.id token, status enum, data.* reveal fields.
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Wiza(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://wiza.co"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "wiza",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "wiza:default"},
		Policy:       provider.CallPolicy{Timeout: 90 * time.Second, MaxAttempts: 1},
		PollInterval: 3 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldMobilePhone, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldFullName, Cost: 5, ExpectedConfidence: 0.95},
			{Field: domain.FieldJobTitle, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldLinkedInURL, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.75},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			rev := map[string]string{}
			putIf(rev, "profile_url", req.Known[domain.FieldLinkedInURL])
			putIf(rev, "full_name", req.Known[domain.FieldFullName])
			putIf(rev, "company", req.Known[domain.FieldCompanyName])
			putIf(rev, "domain", req.Known[domain.FieldCompanyDomain])
			putIf(rev, "email", req.Known[domain.FieldWorkEmail])
			body := map[string]any{"individual_reveal": rev, "enrichment_level": "full"}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/individual_reveals", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.Data.ID == 0 {
				return "", domain.NewProviderError("wiza", domain.ClassTransient, errNoJobID)
			}
			return itoa(p.Data.ID), nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/individual_reveals/"+token, nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Data struct {
					Status          string `json:"status"`
					Email           string `json:"email"`
					EmailStatus     string `json:"email_status"`
					MobilePhone     string `json:"mobile_phone"`
					PhoneStatus     string `json:"phone_status"`
					Name            string `json:"name"`
					Title           string `json:"title"`
					LinkedInURL     string `json:"linkedin_profile_url"`
					Company         string `json:"company"`
					CompanyDomain   string `json:"company_domain"`
					CompanySize     int64  `json:"company_size"`
					CompanyIndustry string `json:"company_industry"`
					CompanyType     string `json:"company_type"`
					CompanyLocality string `json:"company_locality"`
					CompanyCountry  string `json:"company_country"`
					CompanyFounded  int64  `json:"company_founded"`
					CompanyLinkedIn string `json:"company_linkedin"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			d := p.Data
			switch d.Status {
			case "finished":
				// terminal — map below (email may be null on a clean no-match)
			case "queued", "resolving", "":
				return provider.Result{}, false, nil
			default: // failed
				return provider.Result{}, true, nil
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, d.Email, 0.85)
			put(domain.FieldEmailStatus, d.EmailStatus, 0.85)
			put(domain.FieldMobilePhone, d.MobilePhone, 0.85)
			put(domain.FieldPhoneStatus, d.PhoneStatus, 0.80)
			put(domain.FieldFullName, d.Name, 0.95)
			put(domain.FieldJobTitle, d.Title, 0.90)
			put(domain.FieldLinkedInURL, d.LinkedInURL, 0.80)
			put(domain.FieldCompanyName, d.Company, 0.90)
			put(domain.FieldCompanyDomain, d.CompanyDomain, 0.90)
			put(domain.FieldIndustry, d.CompanyIndustry, 0.75)
			put(domain.FieldCompanyType, d.CompanyType, 0.70)
			put(domain.FieldCompanyHQCity, d.CompanyLocality, 0.70)
			put(domain.FieldCompanyHQCountry, d.CompanyCountry, 0.70)
			put(domain.FieldCompanyLinkedInURL, d.CompanyLinkedIn, 0.75)
			if d.CompanySize > 0 {
				put(domain.FieldEmployeeCount, itoa(d.CompanySize), 0.80)
			}
			if d.CompanyFounded > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(d.CompanyFounded), 0.70)
			}
			return res, true, nil
		},
	}
}
