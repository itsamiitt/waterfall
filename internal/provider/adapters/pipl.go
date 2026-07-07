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

// Pipl builds an adapter for the Pipl Search (identity) API (docs/03 §7).
//   - Endpoint: GET {base}?email=&first_name=&last_name=  (base default https://api.pipl.com/search/)
//     [docs.pipl.com; official piplapis client].
//   - Auth: API key in the "key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email / first_name+last_name. Fills identity fields from the `person` object.
//     No match = HTTP 200 with @persons_count:0 and no `person` (yields no values).
//   - Status: DEPRIORITIZED (ADR-0009) — public-web + social + identity-graph provenance.
//
// VERIFIED from docs + the official piplapis client: endpoint, `key` query auth, person schema
// (names/emails[@type]/urls[@domain]/phones[@type]/jobs). mobile display key + field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func Pipl(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.pipl.com/search/"
	}
	return &provider.HTTPAdapter{
		NameV:   "pipl",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "key",
			KeyPoolSelector: "pipl:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldFullName, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldFirstName, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldLastName, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldWorkEmail, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldPersonalEmail, Cost: 6, ExpectedConfidence: 0.65},
			{Field: domain.FieldLinkedInURL, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldMobilePhone, Cost: 6, ExpectedConfidence: 0.60},
			{Field: domain.FieldJobTitle, Cost: 6, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyName, Cost: 6, ExpectedConfidence: 0.60},
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
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Person *struct {
					Names []struct {
						First   string `json:"first"`
						Last    string `json:"last"`
						Display string `json:"display"`
					} `json:"names"`
					Emails []struct {
						Type    string `json:"@type"`
						Address string `json:"address"`
					} `json:"emails"`
					Phones []struct {
						Type        string `json:"@type"`
						DisplayIntl string `json:"display_international"`
					} `json:"phones"`
					URLs []struct {
						Domain string `json:"@domain"`
						URL    string `json:"url"`
					} `json:"urls"`
					Jobs []struct {
						Title        string `json:"title"`
						Organization string `json:"organization"`
					} `json:"jobs"`
				} `json:"person"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Person == nil {
				return res, nil
			}
			pr := p.Person
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			if len(pr.Names) > 0 {
				put(domain.FieldFullName, pr.Names[0].Display, 0.70)
				put(domain.FieldFirstName, pr.Names[0].First, 0.70)
				put(domain.FieldLastName, pr.Names[0].Last, 0.70)
			}
			for _, e := range pr.Emails {
				switch e.Type {
				case "work":
					put(domain.FieldWorkEmail, e.Address, 0.70)
				case "personal":
					put(domain.FieldPersonalEmail, e.Address, 0.65)
				}
			}
			for _, ph := range pr.Phones {
				if ph.Type == "mobile" {
					put(domain.FieldMobilePhone, ph.DisplayIntl, 0.60)
				}
			}
			for _, ur := range pr.URLs {
				if strings.EqualFold(ur.Domain, "linkedin.com") {
					put(domain.FieldLinkedInURL, ur.URL, 0.70)
				}
			}
			if len(pr.Jobs) > 0 {
				put(domain.FieldJobTitle, pr.Jobs[0].Title, 0.65)
				put(domain.FieldCompanyName, pr.Jobs[0].Organization, 0.60)
			}
			return res, nil
		},
	}
}
