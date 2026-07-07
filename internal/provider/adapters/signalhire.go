package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// SignalHire builds an adapter for the SignalHire Person API (docs/03 §7). Although the vendor's
// default mode is async submit→callback (HTTP 201 + results POSTed to a callbackUrl, with NO poll
// endpoint — unusable for a pull-based enricher), it also offers a SYNCHRONOUS single-shot mode:
// POST /candidate/search with withoutWaterfall=true (and no callbackUrl) returns the results array
// [{item,status,candidate}] directly (HTTP 200). So this is a plain HTTPAdapter, not async.
//   - Auth: API key in the "apikey" header, injected at egress.
//   - Input: linkedin_url (items[0]). Fills contact + identity fields from candidate.
//   - Status: DEPRIORITIZED (ADR-0009) — LinkedIn/public-web + crowdsourced (extension) provenance.
//
// VERIFIED from docs: endpoint, apikey auth, withoutWaterfall single-shot, candidate.{fullName,
// contacts[]{type,subType,value},experience[]{position,company,website,current},social[]{type,link}}.
// email_status/phone_status from contacts[].rating (70/100) are NOT true enums, so intentionally
// omitted. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func SignalHire(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://www.signalhire.com/api/v1"
	}
	return &provider.HTTPAdapter{
		NameV:   "signalhire",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "apikey", KeyPoolSelector: "signalhire:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldFullName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.75},
			{Field: domain.FieldPersonalEmail, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldMobilePhone, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldOfficePhone, Cost: 3, ExpectedConfidence: 0.60},
			{Field: domain.FieldJobTitle, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyDomain, Cost: 3, ExpectedConfidence: 0.60},
			{Field: domain.FieldLinkedInURL, Cost: 3, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]any{
				"items":            []string{req.Known[domain.FieldLinkedInURL]},
				"withoutWaterfall": true, // force synchronous single-shot (no callback)
			}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/candidate/search", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var arr []struct {
				Status    string `json:"status"`
				Candidate struct {
					FullName string `json:"fullName"`
					Contacts []struct {
						Type    string `json:"type"`
						SubType string `json:"subType"`
						Value   string `json:"value"`
					} `json:"contacts"`
					Experience []struct {
						Position string `json:"position"`
						Company  string `json:"company"`
						Website  string `json:"website"`
						Current  bool   `json:"current"`
					} `json:"experience"`
					Social []struct {
						Type string `json:"type"`
						Link string `json:"link"`
					} `json:"social"`
				} `json:"candidate"`
			}
			if err := json.Unmarshal(body, &arr); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(arr) == 0 {
				return res, nil
			}
			c := arr[0].Candidate
			put := func(f domain.Field, v string, conf domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: conf}
				}
			}
			put(domain.FieldFullName, c.FullName, 0.90)
			for _, ct := range c.Contacts {
				switch {
				case ct.Type == "email" && ct.SubType == "work":
					put(domain.FieldWorkEmail, ct.Value, 0.75)
				case ct.Type == "email" && ct.SubType == "personal":
					put(domain.FieldPersonalEmail, ct.Value, 0.70)
				case ct.SubType == "mobile":
					put(domain.FieldMobilePhone, ct.Value, 0.70)
				case ct.SubType == "work_phone":
					put(domain.FieldOfficePhone, ct.Value, 0.60)
				}
			}
			for _, e := range c.Experience {
				if e.Current {
					put(domain.FieldJobTitle, e.Position, 0.70)
					put(domain.FieldCompanyName, e.Company, 0.70)
					put(domain.FieldCompanyDomain, bareDomain(e.Website), 0.60)
					break
				}
			}
			for _, s := range c.Social {
				if s.Type == "li" {
					put(domain.FieldLinkedInURL, s.Link, 0.80)
				}
			}
			return res, nil
		},
	}
}
