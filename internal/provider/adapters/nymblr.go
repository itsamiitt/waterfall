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

// Nymblr builds an adapter for the Nymblr (Nimbler) Contact Data API (docs/03 §1) — a US B2B
// contact-and-company enrichment database (~120M US contacts).
//   - Endpoint: POST {base}/append/contact with JSON seed identifiers  (base default
//     https://api.nimbler.com/api/v1) [SwaggerHub NYMBLR/NymblrDataAPIv1].
//   - Auth: bearer token "Authorization: Bearer <key>", injected at egress (AuthBearer).
//   - Input: work_email / linkedin_url / first_name+last_name+company_name / mobile_phone (any seed).
//     Returns a bare Contact object filling the full person + company field set.
//   - Status: DEPRIORITIZED (ADR-0009) — public-web/LinkedIn-sourced B2B contact PII (work+personal
//     email, mobile + direct-dial phone, LinkedIn URLs); needs a compliance review before serving.
//   - Quirk: 404 = "no records found" (the no-match outcome); 403 = "unauthorized request" (auth;
//     the shared map treats 403 as RATE_LIMIT — documented discrepancy). Overlapping schema fields
//     (personalEmail vs contactPersonalEmail, companySic vs companySICCode6) → best-fit, down-weighted.
//
// VERIFIED from the official OpenAPI (SwaggerHub): endpoint, bearer auth, Contact field names. Exact
// runtime population of ambiguous fields pinned UNVERIFIED until a live key (see hunter.go).
func Nymblr(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.nimbler.com/api/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "nymblr",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "nymblr:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldPersonalEmail, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldMobilePhone, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldDirectDial, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldLinkedInURL, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldJobTitle, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldSeniority, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldDepartment, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldFirstName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldLastName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldFullName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldEmployeeCount, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyRevenue, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyFoundedYear, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCountry, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCity, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyPhone, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldNAICS, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldSIC, Cost: 3, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			seed := map[string]string{}
			putIf(seed, "emailAddress", req.Known[domain.FieldWorkEmail])
			putIf(seed, "linkedinUrl", req.Known[domain.FieldLinkedInURL])
			putIf(seed, "firstName", req.Known[domain.FieldFirstName])
			putIf(seed, "lastName", req.Known[domain.FieldLastName])
			putIf(seed, "company", req.Known[domain.FieldCompanyName])
			putIf(seed, "mobilePhone", req.Known[domain.FieldMobilePhone])
			b, err := json.Marshal(seed)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/append/contact", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				ContactEmail         string          `json:"contactEmail"`
				ContactPersonalEmail string          `json:"contactPersonalEmail"`
				ContactPhone         string          `json:"contactPhone"`
				DirectDialPhone      string          `json:"directDialPhone"`
				ContactLinkedinURL   string          `json:"contactLinkedinURL"`
				ContactTitle         string          `json:"contactTitle"`
				ContactTitleLevel    string          `json:"contactTitleLevel"`
				ContactJobFunctions  []string        `json:"contactJobFunctions"`
				ContactFirstName     string          `json:"contactFirstName"`
				ContactLastName      string          `json:"contactLastName"`
				ContactFullName      string          `json:"contactFullName"`
				CompanyName          string          `json:"companyName"`
				CompanyDomain        string          `json:"companyDomain"`
				CompanyLinkedinURL   string          `json:"companyLinkedinURL"`
				CompanyEmployees     json.RawMessage `json:"companyEmployees"`
				CompanyRevenueRange  string          `json:"companyRevenueRange"`
				CompanyPrimaryIndus  string          `json:"companyPrimaryIndustry"`
				CompanyFoundedYear   json.RawMessage `json:"companyFoundedYear"`
				CompanyCountry       string          `json:"companyCountry"`
				CompanyCity          string          `json:"companyCity"`
				CompanyType          string          `json:"companyType"`
				CompanyPhone         string          `json:"companyPhone"`
				CompanyNAICSCode     json.RawMessage `json:"companyNAICSCode"`
				CompanySic           string          `json:"companySic"`
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
			put(domain.FieldWorkEmail, p.ContactEmail, 0.90)
			put(domain.FieldPersonalEmail, p.ContactPersonalEmail, 0.65)
			put(domain.FieldMobilePhone, p.ContactPhone, 0.65)
			put(domain.FieldDirectDial, p.DirectDialPhone, 0.90)
			put(domain.FieldLinkedInURL, p.ContactLinkedinURL, 0.90)
			put(domain.FieldJobTitle, p.ContactTitle, 0.90)
			put(domain.FieldSeniority, p.ContactTitleLevel, 0.90)
			put(domain.FieldDepartment, normList(p.ContactJobFunctions), 0.65)
			put(domain.FieldFirstName, p.ContactFirstName, 0.90)
			put(domain.FieldLastName, p.ContactLastName, 0.90)
			put(domain.FieldFullName, p.ContactFullName, 0.90)
			put(domain.FieldCompanyName, p.CompanyName, 0.90)
			put(domain.FieldCompanyDomain, p.CompanyDomain, 0.90)
			put(domain.FieldCompanyLinkedInURL, p.CompanyLinkedinURL, 0.90)
			put(domain.FieldEmployeeCount, rawStr(p.CompanyEmployees), 0.85)
			put(domain.FieldCompanyRevenue, p.CompanyRevenueRange, 0.65)
			put(domain.FieldIndustry, p.CompanyPrimaryIndus, 0.90)
			put(domain.FieldCompanyFoundedYear, rawStr(p.CompanyFoundedYear), 0.90)
			put(domain.FieldCompanyHQCountry, p.CompanyCountry, 0.80)
			put(domain.FieldCompanyHQCity, p.CompanyCity, 0.80)
			put(domain.FieldCompanyType, p.CompanyType, 0.90)
			put(domain.FieldCompanyPhone, p.CompanyPhone, 0.90)
			put(domain.FieldNAICS, rawStr(p.CompanyNAICSCode), 0.90)
			put(domain.FieldSIC, p.CompanySic, 0.70)
			return res, nil
		},
	}
}
