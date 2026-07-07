package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Demandbase builds an async match→fetch adapter for the Demandbase B2B Data API (docs/03 §7) —
// firmographics (ex-InsideView data).
//   - Match: POST {base}/match with {"requests":[{"id":"1","websites":[domain]}]} →
//     matches[0].companyMatches[0].company.companyId.
//   - Fetch: GET {base}/company/{companyId} → firmographics (done on fetch).
//   - Auth: OAuth2 client-credentials, TokenStyle "json" — the egress injector POSTs
//     {grantType,clientId,clientSecret} (pool secret "clientId:clientSecret") to the token URL,
//     caches the Bearer, injects it on both hops.
//   - base default https://uapi.demandbase.com/data/b2b/v1 [developer.demandbase.com].
//   - Intent (topics/score/surge) is NOT a self-serve read endpoint → firmographics only.
//
// VERIFIED from docs: token endpoint (JSON camelCase creds), match + company/{id} endpoints, company
// fields companyName/websites/industry/employeeCount/revenue/address/naics/sic. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func Demandbase(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://uapi.demandbase.com/data/b2b/v1"
	}
	// The token endpoint lives on the same host but a different path (/auth/v1) — derive it from
	// base's scheme+host so a test/base override points the token exchange at the same server.
	tokenURL := "https://uapi.demandbase.com/auth/v1/token"
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		tokenURL = u.Scheme + "://" + u.Host + "/auth/v1/token"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:   "demandbase",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthOAuth2CC,
			KeyPoolSelector: "demandbase:default",
			TokenURL:        tokenURL,
			TokenStyle:      "json",
		},
		Policy:       provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		PollInterval: time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 4, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 4, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyRevenue, Cost: 4, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyHQCountry, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldNAICS, Cost: 4, ExpectedConfidence: 0.85},
			{Field: domain.FieldSIC, Cost: 4, ExpectedConfidence: 0.85},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]any{"requests": []map[string]any{{"id": "1", "websites": []string{req.Known[domain.FieldCompanyDomain]}}}}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/match", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Matches []struct {
					CompanyMatches []struct {
						Company struct {
							CompanyID json.RawMessage `json:"companyId"`
						} `json:"company"`
					} `json:"companyMatches"`
				} `json:"matches"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if len(p.Matches) == 0 || len(p.Matches[0].CompanyMatches) == 0 {
				return "", domain.NewProviderError("demandbase", domain.ClassNotFound, errResultsGone)
			}
			id := rawStr(p.Matches[0].CompanyMatches[0].Company.CompanyID)
			if id == "" {
				return "", domain.NewProviderError("demandbase", domain.ClassNotFound, errResultsGone)
			}
			return id, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/company/"+token, nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				CompanyName   string   `json:"companyName"`
				Websites      []string `json:"websites"`
				Industry      string   `json:"industry"`
				EmployeeCount int64    `json:"employeeCount"`
				Revenue       int64    `json:"revenue"`
				NAICS         string   `json:"naics"`
				SIC           string   `json:"sic"`
				Address       struct {
					Country string `json:"country"`
					City    string `json:"city"`
				} `json:"address"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, p.CompanyName, 0.90)
			put(domain.FieldIndustry, p.Industry, 0.80)
			put(domain.FieldNAICS, p.NAICS, 0.85)
			put(domain.FieldSIC, p.SIC, 0.85)
			put(domain.FieldCompanyHQCountry, p.Address.Country, 0.85)
			put(domain.FieldCompanyHQCity, p.Address.City, 0.85)
			if len(p.Websites) > 0 {
				put(domain.FieldCompanyDomain, bareDomain(p.Websites[0]), 0.85)
			}
			if p.EmployeeCount > 0 {
				put(domain.FieldEmployeeCount, itoa(p.EmployeeCount), 0.80)
			}
			if p.Revenue > 0 {
				put(domain.FieldCompanyRevenue, itoa(p.Revenue), 0.75)
			}
			return res, true, nil
		},
	}
}
