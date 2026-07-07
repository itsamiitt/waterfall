package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Apollo builds an adapter for the Apollo.io People Enrichment ("People Match") API (docs/03 §7).
//   - Endpoint: POST {base} with a JSON body of match keys (base default
//     https://api.apollo.io/api/v1/people/match) [docs.apollo.io/reference/people-enrichment].
//   - Auth: API key in the "X-Api-Key" header, injected at egress (AuthAPIKeyHeader). (Apollo
//     removed api_key query/body support in Sept 2024 — header is mandatory.)
//   - Input: work_email, first_name, last_name, full_name, company_name, company_domain,
//     linkedin_url. Fills the person + current-company fields Apollo returns WITHOUT a paid reveal
//     flag (email, email_status, title, seniority, LinkedIn, firmographics). mobile/personal-email
//     require reveal_* flags (extra credits; phone reveal is async) and are intentionally NOT
//     requested here.
//   - work_email confidence is lifted to 0.90 when person.email_status == "verified", else 0.72.
//   - Status: DEPRIORITIZED (ADR-0009) — real licensed API but LinkedIn/public-web provenance;
//     off by default until a per-provider compliance review.
//
// VERIFIED from docs: endpoint, X-Api-Key header, request params, person/organization response
// shape. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Apollo(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.apollo.io/api/v1/people/match"
	}
	return &provider.HTTPAdapter{
		NameV:   "apollo",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-Api-Key",
			KeyPoolSelector: "apollo:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 9, ExpectedConfidence: 0.72},
			{Field: domain.FieldEmailStatus, Cost: 9, ExpectedConfidence: 0.90},
			{Field: domain.FieldLinkedInURL, Cost: 9, ExpectedConfidence: 0.85},
			{Field: domain.FieldJobTitle, Cost: 9, ExpectedConfidence: 0.80},
			{Field: domain.FieldSeniority, Cost: 9, ExpectedConfidence: 0.78},
			{Field: domain.FieldFullName, Cost: 9, ExpectedConfidence: 0.88},
			{Field: domain.FieldCompanyName, Cost: 9, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 9, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 9, ExpectedConfidence: 0.72},
			{Field: domain.FieldIndustry, Cost: 9, ExpectedConfidence: 0.75},
			{Field: domain.FieldOfficePhone, Cost: 9, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload := map[string]string{}
			putIf(payload, "email", req.Known[domain.FieldWorkEmail])
			putIf(payload, "first_name", req.Known[domain.FieldFirstName])
			putIf(payload, "last_name", req.Known[domain.FieldLastName])
			putIf(payload, "name", req.Known[domain.FieldFullName])
			putIf(payload, "organization_name", req.Known[domain.FieldCompanyName])
			putIf(payload, "domain", req.Known[domain.FieldCompanyDomain])
			putIf(payload, "linkedin_url", req.Known[domain.FieldLinkedInURL])
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
		Decode: decodeApollo,
	}
}

func decodeApollo(body []byte) (provider.Result, error) {
	var p struct {
		Person struct {
			Email        string `json:"email"`
			EmailStatus  string `json:"email_status"`
			LinkedInURL  string `json:"linkedin_url"`
			Title        string `json:"title"`
			Seniority    string `json:"seniority"`
			Name         string `json:"name"`
			Organization struct {
				Name          string `json:"name"`
				PrimaryDomain string `json:"primary_domain"`
				Phone         string `json:"phone"`
				Industry      string `json:"industry"`
				NumEmployees  int64  `json:"estimated_num_employees"`
			} `json:"organization"`
		} `json:"person"`
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
	per := p.Person
	if per.Email != "" {
		conf := domain.Confidence(0.72)
		if per.EmailStatus == "verified" {
			conf = 0.90
		}
		put(domain.FieldWorkEmail, per.Email, conf)
	}
	put(domain.FieldEmailStatus, per.EmailStatus, 0.90)
	put(domain.FieldLinkedInURL, per.LinkedInURL, 0.85)
	put(domain.FieldJobTitle, per.Title, 0.80)
	put(domain.FieldSeniority, per.Seniority, 0.78)
	put(domain.FieldFullName, per.Name, 0.88)
	put(domain.FieldCompanyName, per.Organization.Name, 0.85)
	put(domain.FieldCompanyDomain, per.Organization.PrimaryDomain, 0.85)
	put(domain.FieldOfficePhone, per.Organization.Phone, 0.70)
	put(domain.FieldIndustry, per.Organization.Industry, 0.75)
	if per.Organization.NumEmployees > 0 {
		put(domain.FieldEmployeeCount, strconv.FormatInt(per.Organization.NumEmployees, 10), 0.72)
	}
	return res, nil
}

// putIf adds k=v to m only when v is non-empty.
func putIf(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}
