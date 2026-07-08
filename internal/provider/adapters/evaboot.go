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

// Evaboot builds an adapter for the Evaboot Email Finder API (docs/03 §2).
//   - Endpoint: POST {base}/v1/email-finder/single/ with JSON {first_name,last_name,company_name,
//     company_domain}  (base default https://api.evaboot.com) [docs.evaboot.com OpenAPI 3.0.3].
//   - Auth: bearer token "Authorization: Bearer <key>", injected at egress (AuthBearer).
//   - Input: first_name+last_name (+company_name/company_domain). Fills work_email (found_email),
//     email_status (email_validity: safe|riskier|null), full_name/first_name/last_name, company_name/
//     domain (echoed).
//   - Status: DEPRIORITIZED (ADR-0009) — Evaboot's data is LinkedIn/Sales-Navigator + web scraping
//     with pattern-guessed emails; needs a compliance review before serving.
//   - Quirk: no-match is returned as HTTP 400 with error.type="EmailFinderError" (the shared map
//     classifies 400 as BAD_REQUEST — so a no-match surfaces as a bad-request error, documented);
//     402 = insufficient credits (QUOTA).
//
// VERIFIED from the vendor's published OpenAPI: endpoint, bearer auth, prospect.* field names,
// email_validity enum. Exact values pinned UNVERIFIED until a live authorized call (see hunter.go).
func Evaboot(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.evaboot.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "evaboot",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "evaboot:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.75},
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.85},
			{Field: domain.FieldFullName, Cost: 1, ExpectedConfidence: 0.95},
			{Field: domain.FieldFirstName, Cost: 1, ExpectedConfidence: 0.95},
			{Field: domain.FieldLastName, Cost: 1, ExpectedConfidence: 0.95},
			{Field: domain.FieldCompanyName, Cost: 1, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.60},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			seed := map[string]string{}
			putIf(seed, "first_name", req.Known[domain.FieldFirstName])
			putIf(seed, "last_name", req.Known[domain.FieldLastName])
			putIf(seed, "company_name", req.Known[domain.FieldCompanyName])
			putIf(seed, "company_domain", req.Known[domain.FieldCompanyDomain])
			b, err := json.Marshal(seed)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/v1/email-finder/single/", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Prospect struct {
					FoundEmail    string `json:"found_email"`
					EmailValidity string `json:"email_validity"`
					FullName      string `json:"full_name"`
					FirstName     string `json:"first_name"`
					LastName      string `json:"last_name"`
					CompanyName   string `json:"company_name"`
					CompanyDomain string `json:"company_domain"`
				} `json:"prospect"`
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
			put(domain.FieldWorkEmail, p.Prospect.FoundEmail, 0.75)
			put(domain.FieldEmailStatus, p.Prospect.EmailValidity, 0.85)
			put(domain.FieldFullName, p.Prospect.FullName, 0.95)
			put(domain.FieldFirstName, p.Prospect.FirstName, 0.95)
			put(domain.FieldLastName, p.Prospect.LastName, 0.95)
			put(domain.FieldCompanyName, p.Prospect.CompanyName, 0.60)
			put(domain.FieldCompanyDomain, p.Prospect.CompanyDomain, 0.60)
			return res, nil
		},
	}
}
