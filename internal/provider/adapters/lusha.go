package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Lusha builds an adapter for the Lusha v3 Contact Search-and-Enrich API (docs/03 §7).
//   - Endpoint: POST {base} with JSON {contacts:[{...identifiers...}], reveal:["emails","phones"]}
//     (base default https://api.lusha.com/v3/contacts/search-and-enrich) [docs.lusha.com].
//   - Auth: API key in the "api_key" header (not Bearer/query), injected at egress.
//   - Input: work_email / linkedin_url / first+last+(company_name|company_domain). Fills contact
//     emails (by emailType) + phones (by phoneType) + job title/seniority/department + firmographics.
//   - Status: DEPRIORITIZED (ADR-0009) — community/web-derived contact provenance.
//
// VERIFIED from docs + Lusha's OSS n8n node: endpoint, api_key auth, request body, leaf field names.
// The enriched-response container path (contacts[].data.*) and emailType/phoneType literals are a
// reconstruction — pinned UNVERIFIED until a live authorized call (see hunter.go).
func Lusha(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.lusha.com/v3/contacts/search-and-enrich"
	}
	return &provider.HTTPAdapter{
		NameV:   "lusha",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "api_key",
			KeyPoolSelector: "lusha:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldPersonalEmail, Cost: 8, ExpectedConfidence: 0.70},
			{Field: domain.FieldMobilePhone, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldDirectDial, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldJobTitle, Cost: 8, ExpectedConfidence: 0.78},
			{Field: domain.FieldSeniority, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldDepartment, Cost: 8, ExpectedConfidence: 0.72},
			{Field: domain.FieldLinkedInURL, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldFullName, Cost: 8, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyName, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyDomain, Cost: 8, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			contact := map[string]string{}
			putIf(contact, "email", req.Known[domain.FieldWorkEmail])
			putIf(contact, "linkedinUrl", req.Known[domain.FieldLinkedInURL])
			putIf(contact, "firstName", req.Known[domain.FieldFirstName])
			putIf(contact, "lastName", req.Known[domain.FieldLastName])
			putIf(contact, "companyName", req.Known[domain.FieldCompanyName])
			putIf(contact, "companyDomain", req.Known[domain.FieldCompanyDomain])
			body := map[string]any{
				"contacts": []map[string]string{contact},
				"reveal":   []string{"emails", "phones"},
			}
			b, err := json.Marshal(body)
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
		Decode: decodeLusha,
	}
}

func decodeLusha(body []byte) (provider.Result, error) {
	var p struct {
		Contacts []struct {
			Data struct {
				EmailAddresses []struct {
					Email     string `json:"email"`
					EmailType string `json:"emailType"`
				} `json:"emailAddresses"`
				PhoneNumbers []struct {
					Number    string `json:"number"`
					PhoneType string `json:"phoneType"`
				} `json:"phoneNumbers"`
				JobTitle struct {
					Title       string   `json:"title"`
					Seniority   string   `json:"seniority"`
					Departments []string `json:"departments"`
				} `json:"jobTitle"`
				SocialLinks struct {
					LinkedIn string `json:"linkedin"`
				} `json:"socialLinks"`
				FullName string `json:"fullName"`
				Company  struct {
					Name string `json:"name"`
					FQDN string `json:"fqdn"`
				} `json:"company"`
			} `json:"data"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if len(p.Contacts) == 0 {
		return res, nil
	}
	d := p.Contacts[0].Data
	put := func(f domain.Field, v string, c domain.Confidence) {
		if v != "" {
			res.Values[f] = provider.Observation{Value: v, Confidence: c}
		}
	}
	for _, e := range d.EmailAddresses {
		switch e.EmailType {
		case "work", "professional":
			put(domain.FieldWorkEmail, e.Email, 0.75)
		case "personal":
			put(domain.FieldPersonalEmail, e.Email, 0.70)
		}
	}
	for _, ph := range d.PhoneNumbers {
		switch ph.PhoneType {
		case "mobile":
			put(domain.FieldMobilePhone, ph.Number, 0.80)
		case "direct", "work":
			put(domain.FieldDirectDial, ph.Number, 0.75)
		}
	}
	put(domain.FieldJobTitle, d.JobTitle.Title, 0.78)
	put(domain.FieldSeniority, d.JobTitle.Seniority, 0.75)
	if len(d.JobTitle.Departments) > 0 {
		put(domain.FieldDepartment, d.JobTitle.Departments[0], 0.72)
	}
	put(domain.FieldLinkedInURL, d.SocialLinks.LinkedIn, 0.80)
	put(domain.FieldFullName, d.FullName, 0.85)
	put(domain.FieldCompanyName, d.Company.Name, 0.75)
	put(domain.FieldCompanyDomain, d.Company.FQDN, 0.75)
	return res, nil
}
