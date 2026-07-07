package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// AdaptIO builds an adapter for the Adapt.io v3 Contact Enrich API (docs/03 §7).
//   - Endpoint: POST {base}/contact/enrich with {email | linkedinURL | firstName+lastName+domain}
//   - include:["EMAIL","PHONE"]  (base default https://api.adapt.io/v3) [adapt.io/api-docs/v3].
//   - Auth: TWO required headers — "email" (account email) + "apiKey" — via AuthAPIKeyDualHeader;
//     the pool secret is "accountEmail:apiKey", split at egress.
//   - No match = HTTP 200 with body code APP-200-002 (data absent → yields no values, not an error).
//
// VERIFIED from docs: base, dual-header auth, /contact/enrich, response data.{email,phoneNumber[]
// {type,number},linkedin,title,level,company{name,website,headCount,industry,revenue},firstName,
// lastName}. emailDeliverabilityScore is an int (not a status enum) → email_status not mapped. Field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func AdaptIO(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.adapt.io/v3"
	}
	return &provider.HTTPAdapter{
		NameV:   "adapt-io",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:           provider.AuthAPIKeyDualHeader,
			HeaderName:       "email",
			SecondHeaderName: "apiKey",
			KeyPoolSelector:  "adapt-io:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldMobilePhone, Cost: 4, ExpectedConfidence: 0.72},
			{Field: domain.FieldDirectDial, Cost: 4, ExpectedConfidence: 0.70},
			{Field: domain.FieldLinkedInURL, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldJobTitle, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldSeniority, Cost: 4, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyName, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 4, ExpectedConfidence: 0.70},
			{Field: domain.FieldIndustry, Cost: 4, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyRevenue, Cost: 4, ExpectedConfidence: 0.70},
			{Field: domain.FieldFirstName, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldLastName, Cost: 4, ExpectedConfidence: 0.85},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			m := map[string]any{"include": []string{"EMAIL", "PHONE"}}
			setStr := func(k, v string) {
				if v != "" {
					m[k] = v
				}
			}
			setStr("email", req.Known[domain.FieldWorkEmail])
			setStr("linkedinURL", req.Known[domain.FieldLinkedInURL])
			setStr("firstName", req.Known[domain.FieldFirstName])
			setStr("lastName", req.Known[domain.FieldLastName])
			setStr("domain", req.Known[domain.FieldCompanyDomain])
			b, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/contact/enrich", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					Email       string `json:"email"`
					PhoneNumber []struct {
						Type   string `json:"type"`
						Number string `json:"number"`
					} `json:"phoneNumber"`
					LinkedIn  string `json:"linkedin"`
					Title     string `json:"title"`
					Level     string `json:"level"`
					FirstName string `json:"firstName"`
					LastName  string `json:"lastName"`
					Company   struct {
						Name      string          `json:"name"`
						Website   string          `json:"website"`
						HeadCount json.RawMessage `json:"headCount"`
						Industry  string          `json:"industry"`
						Revenue   json.RawMessage `json:"revenue"`
					} `json:"company"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			d := p.Data
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, d.Email, 0.80)
			put(domain.FieldLinkedInURL, d.LinkedIn, 0.80)
			put(domain.FieldJobTitle, d.Title, 0.80)
			put(domain.FieldSeniority, d.Level, 0.75)
			put(domain.FieldFirstName, d.FirstName, 0.85)
			put(domain.FieldLastName, d.LastName, 0.85)
			put(domain.FieldCompanyName, d.Company.Name, 0.80)
			put(domain.FieldCompanyDomain, bareDomain(d.Company.Website), 0.80)
			put(domain.FieldIndustry, d.Company.Industry, 0.75)
			put(domain.FieldEmployeeCount, rawStr(d.Company.HeadCount), 0.70)
			put(domain.FieldCompanyRevenue, rawStr(d.Company.Revenue), 0.70)
			for _, ph := range d.PhoneNumber {
				switch ph.Type {
				case "mobile_number":
					put(domain.FieldMobilePhone, ph.Number, 0.72)
				case "direct_line":
					put(domain.FieldDirectDial, ph.Number, 0.70)
				}
			}
			return res, nil
		},
	}
}
