package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Amplemarket builds an async adapter for the Amplemarket people-enrichment API (docs/03 §1).
//   - Submit: POST {base}/people/enrichment-requests with {leads:[{email,linkedin_url,company_domain,
//     company_name,name,title}], reveal_email, reveal_phone_numbers} → 202 {id}.
//   - Poll: GET {base}/people/enrichment-requests/{id} until status=="completed" (queued/processing
//     while running); results[0].result carries the person + nested company object.
//   - Auth: bearer token "Authorization: Bearer <key>", injected at egress (AuthBearer).
//   - base default https://api.amplemarket.com [docs.amplemarket.com, full rendered response example].
//   - Fills work_email, phone by type (mobile/direct/office), linkedin_url, name fields, job_title,
//     and company.* firmographics (domain via website→bareDomain, employee_count, industry, revenue
//     enum, funding_stage, founded_year, hq city/country, type, naics/sic arrays, technologies).
//   - Status: DEPRIORITIZED (ADR-0009) — Amplemarket's own docs state the DB is "solely based on
//     publicly available professional profiles" (public-web provenance); needs a compliance review.
//   - Quirk: 451 = record withheld for legal/GDPR reasons; 404 = person_not_found; per-lead status
//     enriched|not_found|gdpr_removed.
//
// VERIFIED from docs (incl. a full response example): submit/poll endpoints, bearer auth, results[].
// result.* + company.* field names. Exact values pinned UNVERIFIED until a live key (see hunter.go).
func Amplemarket(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.amplemarket.com"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "amplemarket",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "amplemarket:default"},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 5 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 2, ExpectedConfidence: 0.85},
			{Field: domain.FieldDirectDial, Cost: 2, ExpectedConfidence: 0.75},
			{Field: domain.FieldOfficePhone, Cost: 2, ExpectedConfidence: 0.75},
			{Field: domain.FieldLinkedInURL, Cost: 2, ExpectedConfidence: 0.95},
			{Field: domain.FieldJobTitle, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldFirstName, Cost: 2, ExpectedConfidence: 0.95},
			{Field: domain.FieldLastName, Cost: 2, ExpectedConfidence: 0.95},
			{Field: domain.FieldFullName, Cost: 2, ExpectedConfidence: 0.95},
			{Field: domain.FieldCompanyName, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 2, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldEmployeeCount, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldIndustry, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyRevenue, Cost: 2, ExpectedConfidence: 0.85},
			{Field: domain.FieldFundingStage, Cost: 2, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyFoundedYear, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCountry, Cost: 2, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyHQCity, Cost: 2, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyType, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldNAICS, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldSIC, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldTechnographics, Cost: 2, ExpectedConfidence: 0.75},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			lead := map[string]string{}
			putIf(lead, "email", req.Known[domain.FieldWorkEmail])
			putIf(lead, "linkedin_url", req.Known[domain.FieldLinkedInURL])
			putIf(lead, "company_domain", req.Known[domain.FieldCompanyDomain])
			putIf(lead, "company_name", req.Known[domain.FieldCompanyName])
			putIf(lead, "name", req.Known[domain.FieldFullName])
			putIf(lead, "title", req.Known[domain.FieldJobTitle])
			body := map[string]any{
				"leads":                []any{lead},
				"reveal_email":         true,
				"reveal_phone_numbers": true,
			}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/people/enrichment-requests", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			id := rawStr(p.ID)
			if id == "" {
				return "", domain.NewProviderError("amplemarket", domain.ClassTransient, errNoJobID)
			}
			return id, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet,
				strings.TrimRight(base, "/")+"/people/enrichment-requests/"+token, nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Status  string `json:"status"`
				Results []struct {
					Result struct {
						Email        string `json:"email"`
						FirstName    string `json:"first_name"`
						LastName     string `json:"last_name"`
						Name         string `json:"name"`
						Title        string `json:"title"`
						LinkedinURL  string `json:"linkedin_url"`
						PhoneNumbers []struct {
							Number string `json:"number"`
							Type   string `json:"type"`
						} `json:"phone_numbers"`
						Company struct {
							Name             string   `json:"name"`
							Website          string   `json:"website"`
							LinkedinURL      string   `json:"linkedin_url"`
							Industry         string   `json:"industry"`
							Type             string   `json:"type"`
							Employees        int64    `json:"estimated_number_of_employees"`
							EstimatedRevenue string   `json:"estimated_revenue"`
							FundingStage     string   `json:"latest_funding_stage"`
							FoundedYear      int64    `json:"founded_year"`
							NAICSCodes       []string `json:"naics_codes"`
							SICCodes         []string `json:"sic_codes"`
							Technologies     []string `json:"technologies"`
							LocationDetails  struct {
								City    string `json:"city"`
								Country string `json:"country"`
							} `json:"location_details"`
						} `json:"company"`
					} `json:"result"`
				} `json:"results"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if p.Status != "completed" {
				return provider.Result{}, false, nil // queued/processing
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Results) == 0 {
				return res, true, nil
			}
			r := p.Results[0].Result
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, r.Email, 0.90)
			put(domain.FieldLinkedInURL, r.LinkedinURL, 0.95)
			put(domain.FieldJobTitle, r.Title, 0.90)
			put(domain.FieldFirstName, r.FirstName, 0.95)
			put(domain.FieldLastName, r.LastName, 0.95)
			put(domain.FieldFullName, r.Name, 0.95)
			// Phone numbers by type (first of each).
			for _, ph := range r.PhoneNumbers {
				switch ph.Type {
				case "mobile":
					if res.Values[domain.FieldMobilePhone].Value == "" {
						put(domain.FieldMobilePhone, ph.Number, 0.85)
					}
				case "direct":
					if res.Values[domain.FieldDirectDial].Value == "" {
						put(domain.FieldDirectDial, ph.Number, 0.75)
					}
				case "office", "landline":
					if res.Values[domain.FieldOfficePhone].Value == "" {
						put(domain.FieldOfficePhone, ph.Number, 0.75)
					}
				}
			}
			c := r.Company
			put(domain.FieldCompanyName, c.Name, 0.90)
			put(domain.FieldCompanyDomain, bareDomain(c.Website), 0.65)
			put(domain.FieldCompanyLinkedInURL, c.LinkedinURL, 0.90)
			put(domain.FieldIndustry, c.Industry, 0.80)
			put(domain.FieldCompanyType, c.Type, 0.80)
			if c.Employees > 0 {
				put(domain.FieldEmployeeCount, itoa(c.Employees), 0.90)
			}
			put(domain.FieldCompanyRevenue, c.EstimatedRevenue, 0.85)
			put(domain.FieldFundingStage, c.FundingStage, 0.85)
			if c.FoundedYear > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(c.FoundedYear), 0.90)
			}
			put(domain.FieldCompanyHQCountry, c.LocationDetails.Country, 0.75)
			put(domain.FieldCompanyHQCity, c.LocationDetails.City, 0.75)
			put(domain.FieldNAICS, normList(c.NAICSCodes), 0.80)
			put(domain.FieldSIC, normList(c.SICCodes), 0.80)
			put(domain.FieldTechnographics, normList(c.Technologies), 0.75)
			return res, true, nil
		},
	}
}
