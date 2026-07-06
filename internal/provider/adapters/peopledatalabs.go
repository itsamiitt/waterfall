package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// PeopleDataLabs builds an adapter for the People Data Labs Person Enrichment API v5 (docs/03).
//   - Endpoint: GET {base}?first_name=&last_name=&company=&email=&profile=&min_likelihood=
//     (base default https://api.peopledatalabs.com/v5/person/enrich).
//   - Auth: API key in the "X-Api-Key" header, injected at egress (AuthAPIKeyHeader)
//     [https://docs.peopledatalabs.com/docs/authentication].
//   - Quirk: none beyond the standard map — 404 => no match => ClassNotFound (success-with-no-value)
//     [https://docs.peopledatalabs.com/docs/reference-person-enrichment-api].
//   - Match keys: first_name, last_name, company (from company_domain), work_email, linkedin_url.
//   - Fills the matched person + current-company identity fields; per-value confidence is derived
//     from the response `likelihood` (1..10 -> /10).
//
// VERIFIED from official docs: endpoint, X-Api-Key auth, likelihood 1-10, 404 no-match. The exact
// response FIELD NAMES (work_email, mobile_phone, job_company_*) are the documented v5 schema but
// pinned UNVERIFIED per the no-fabrication rule until confirmed against a live authorized call
// (see hunter.go + testdata/README_UNVERIFIED.md).
func PeopleDataLabs(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.peopledatalabs.com/v5/person/enrich"
	}
	return &provider.HTTPAdapter{
		NameV:   "people-data-labs",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-Api-Key",
			KeyPoolSelector: "people-data-labs:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 6, ExpectedConfidence: 0.80},
			{Field: domain.FieldMobilePhone, Cost: 6, ExpectedConfidence: 0.75},
			{Field: domain.FieldJobTitle, Cost: 6, ExpectedConfidence: 0.82},
			{Field: domain.FieldLinkedInURL, Cost: 6, ExpectedConfidence: 0.85},
			{Field: domain.FieldFullName, Cost: 6, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyName, Cost: 6, ExpectedConfidence: 0.82},
			{Field: domain.FieldCompanyDomain, Cost: 6, ExpectedConfidence: 0.82},
			{Field: domain.FieldIndustry, Cost: 6, ExpectedConfidence: 0.75},
			{Field: domain.FieldEmployeeCount, Cost: 6, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "first_name", req.Known[domain.FieldFirstName])
			setIf(q, "last_name", req.Known[domain.FieldLastName])
			setIf(q, "company", req.Known[domain.FieldCompanyDomain])
			setIf(q, "email", req.Known[domain.FieldWorkEmail])
			setIf(q, "profile", req.Known[domain.FieldLinkedInURL])
			q.Set("min_likelihood", "2")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: decodePDL,
	}
}

func decodePDL(body []byte) (provider.Result, error) {
	var p struct {
		Status     int `json:"status"`
		Likelihood int `json:"likelihood"`
		Data       struct {
			FullName           string `json:"full_name"`
			WorkEmail          string `json:"work_email"`
			MobilePhone        string `json:"mobile_phone"`
			JobTitle           string `json:"job_title"`
			LinkedInURL        string `json:"linkedin_url"`
			JobCompanyName     string `json:"job_company_name"`
			JobCompanyWebsite  string `json:"job_company_website"`
			JobCompanyIndustry string `json:"job_company_industry"`
			JobCompanySize     string `json:"job_company_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	// likelihood is 1..10; a 200 body without it still carries matched data, so floor to a modest
	// prior rather than 0.
	conf := domain.Confidence(float64(p.Likelihood) / 10.0).Clamp()
	if p.Likelihood == 0 {
		conf = 0.75
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	set := func(f domain.Field, v string) {
		if v != "" {
			res.Values[f] = provider.Observation{Value: v, Confidence: conf}
		}
	}
	set(domain.FieldWorkEmail, p.Data.WorkEmail)
	set(domain.FieldMobilePhone, p.Data.MobilePhone)
	set(domain.FieldJobTitle, p.Data.JobTitle)
	set(domain.FieldLinkedInURL, p.Data.LinkedInURL)
	set(domain.FieldFullName, p.Data.FullName)
	set(domain.FieldCompanyName, p.Data.JobCompanyName)
	set(domain.FieldCompanyDomain, p.Data.JobCompanyWebsite)
	set(domain.FieldIndustry, p.Data.JobCompanyIndustry)
	set(domain.FieldEmployeeCount, p.Data.JobCompanySize)
	return res, nil
}

// setIf adds k=v to q only when v is non-empty, so absent match keys are omitted from the query.
func setIf(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}
