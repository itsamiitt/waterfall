package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// SalesIntel builds an adapter for the SalesIntel People/Contact API (docs/03 §7) — human-verified
// contacts + direct dials.
//   - Endpoint: GET {base}?email=&first_name=&last_name=&company_domain=&linkedin_url=&job_title=
//     (base default https://api.salesintel.io/service/people) [developer.salesintel.io].
//   - Auth: API key in the "X-CB-ApiKey" header (+ Accept: application/json), injected at egress.
//   - Input: work_email / first+last / company_domain / linkedin_url / mobile_phone / job_title.
//     Fills contact + phone (by phone_numbers[].type: mobile→mobile_phone, work→direct_dial,
//     work_hq/branch→office_phone) + firmographics. No match = 200 with empty search_results[].
//   - Quirk: 403 = account lacks permission (auth, not throttle) — shared map treats 403 as
//     RATE_LIMIT; noted UNVERIFIED.
//
// VERIFIED from docs: endpoint, X-CB-ApiKey auth, request params, response schema (field types).
// Values are the documented schema (no populated example) — pinned UNVERIFIED until a live call.
func SalesIntel(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.salesintel.io/service/people"
	}
	return &provider.HTTPAdapter{
		NameV:   "salesintel",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-CB-ApiKey",
			KeyPoolSelector: "salesintel:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldPersonalEmail, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldMobilePhone, Cost: 8, ExpectedConfidence: 0.85},
			{Field: domain.FieldDirectDial, Cost: 8, ExpectedConfidence: 0.85},
			{Field: domain.FieldOfficePhone, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldJobTitle, Cost: 8, ExpectedConfidence: 0.78},
			{Field: domain.FieldSeniority, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldDepartment, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldLinkedInURL, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldFullName, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyName, Cost: 8, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyDomain, Cost: 8, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 8, ExpectedConfidence: 0.65},
			{Field: domain.FieldNAICS, Cost: 8, ExpectedConfidence: 0.65},
			{Field: domain.FieldSIC, Cost: 8, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "email", req.Known[domain.FieldWorkEmail])
			setIf(q, "first_name", req.Known[domain.FieldFirstName])
			setIf(q, "last_name", req.Known[domain.FieldLastName])
			setIf(q, "company_domain", req.Known[domain.FieldCompanyDomain])
			setIf(q, "linkedin_url", req.Known[domain.FieldLinkedInURL])
			setIf(q, "job_title", req.Known[domain.FieldJobTitle])
			u.RawQuery = q.Encode()
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				return nil, err
			}
			r.Header.Set("Accept", "application/json")
			return r, nil
		},
		Decode: decodeSalesIntel,
	}
}

func decodeSalesIntel(body []byte) (provider.Result, error) {
	var p struct {
		SearchResults []struct {
			Email         string `json:"email"`
			PersonalEmail string `json:"personal_email"`
			PhoneNumbers  []struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"phone_numbers"`
			JobTitle       string `json:"job_title"`
			JobLevel       string `json:"job_level"`
			JobDepartment  string `json:"job_department"`
			SocialProfiles []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"social_profiles"`
			DisplayName    string   `json:"display_name"`
			CompanyName    string   `json:"company_name"`
			CompanyDomains []string `json:"company_domains"`
			Industry       string   `json:"industry"`
			NAICSCode      string   `json:"naics_code"`
			SICCode        string   `json:"sic_code"`
		} `json:"search_results"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if len(p.SearchResults) == 0 {
		return res, nil
	}
	r := p.SearchResults[0]
	put := func(f domain.Field, v string, c domain.Confidence) {
		if v != "" {
			res.Values[f] = provider.Observation{Value: v, Confidence: c}
		}
	}
	put(domain.FieldWorkEmail, r.Email, 0.80)
	put(domain.FieldPersonalEmail, r.PersonalEmail, 0.75)
	put(domain.FieldJobTitle, r.JobTitle, 0.78)
	put(domain.FieldSeniority, r.JobLevel, 0.75)
	put(domain.FieldDepartment, r.JobDepartment, 0.75)
	put(domain.FieldFullName, r.DisplayName, 0.80)
	put(domain.FieldCompanyName, r.CompanyName, 0.65)
	put(domain.FieldIndustry, r.Industry, 0.65)
	put(domain.FieldNAICS, r.NAICSCode, 0.65)
	put(domain.FieldSIC, r.SICCode, 0.65)
	if len(r.CompanyDomains) > 0 {
		put(domain.FieldCompanyDomain, r.CompanyDomains[0], 0.65)
	}
	for _, ph := range r.PhoneNumbers {
		switch ph.Type {
		case "mobile":
			put(domain.FieldMobilePhone, ph.Value, 0.85)
		case "work":
			put(domain.FieldDirectDial, ph.Value, 0.85)
		case "work_hq", "work_branch":
			put(domain.FieldOfficePhone, ph.Value, 0.80)
		}
	}
	for _, sp := range r.SocialProfiles {
		if sp.Type == "linkedin" {
			put(domain.FieldLinkedInURL, sp.URL, 0.75)
		}
	}
	return res, nil
}
