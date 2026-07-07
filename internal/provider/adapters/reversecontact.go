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

// ReverseContact builds an adapter for the Reverse Contact v2 person-enrichment API (docs/03 §7) —
// reverse-email → person (LinkedIn-sourced).
//   - Endpoint: POST {base}/v2/enrich/persons with {"email":…} (also accepts firstName/lastName/
//     companyDomain/companyName)  (base default https://api.reversecontact.com) [docs.reversecontact.com].
//   - Auth: Bearer token (rc_… keys), injected at egress. No match = HTTP 404 (free, 0 credits).
//   - Fills the person block; company-side firmographics need a second /v2/fetch/companies call and
//     are intentionally out of scope for this single-shot slice.
//   - Status: DEPRIORITIZED (ADR-0009) — LinkedIn profile provenance.
//
// VERIFIED from live probes + docs: base/v2, Bearer auth, /v2/enrich/persons, {success,data.person:
// {firstName,lastName,currentPositionTitle,linkedinUrl,currentCompanyName},error} envelope. Field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func ReverseContact(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.reversecontact.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "reverse-contact",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "reverse-contact:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldFirstName, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldLastName, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldFullName, Cost: 4, ExpectedConfidence: 0.78},
			{Field: domain.FieldJobTitle, Cost: 4, ExpectedConfidence: 0.78},
			{Field: domain.FieldLinkedInURL, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyName, Cost: 4, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			m := map[string]string{}
			putIf(m, "email", req.Known[domain.FieldWorkEmail])
			putIf(m, "firstName", req.Known[domain.FieldFirstName])
			putIf(m, "lastName", req.Known[domain.FieldLastName])
			putIf(m, "companyDomain", req.Known[domain.FieldCompanyDomain])
			putIf(m, "companyName", req.Known[domain.FieldCompanyName])
			b, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v2/enrich/persons", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					Person struct {
						FirstName            string `json:"firstName"`
						LastName             string `json:"lastName"`
						CurrentPositionTitle string `json:"currentPositionTitle"`
						LinkedInURL          string `json:"linkedinUrl"`
						CurrentCompanyName   string `json:"currentCompanyName"`
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
			put(domain.FieldFirstName, pr.FirstName, 0.80)
			put(domain.FieldLastName, pr.LastName, 0.80)
			put(domain.FieldJobTitle, pr.CurrentPositionTitle, 0.78)
			put(domain.FieldLinkedInURL, pr.LinkedInURL, 0.85)
			put(domain.FieldCompanyName, pr.CurrentCompanyName, 0.75)
			if pr.FirstName != "" && pr.LastName != "" {
				put(domain.FieldFullName, strings.TrimSpace(pr.FirstName+" "+pr.LastName), 0.78)
			}
			return res, nil
		},
	}
}
