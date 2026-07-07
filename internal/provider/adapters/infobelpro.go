package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// InfobelPRO builds an adapter for the Infobel DaaS API (docs/03 §7) — global registry-derived
// firmographics. Modeled as a single-shot search with returnFirstPage=true, which echoes the first
// page of matched records inline in the POST response (avoiding the separate status-poll flow).
//   - Endpoint: POST {base}/api/search with {"Website":[domain],"BusinessName":[name],
//     "returnFirstPage":true}  (base default https://getdata.infobelpro.com) [getdata.infobelpro.com/Help].
//   - Auth: OAuth2 resource-owner PASSWORD grant (TokenStyle "password"): the egress injector POSTs
//     grant_type=password&username&password (pool secret "username:password") to {base}/api/token,
//     caches the Bearer (~1799s), injects it. Adapter holds no secret.
//   - Fills firmographics from FirstPageRecords[0]. Classification is Infobel/ISIC/NACE (no NAICS/SIC).
//
// VERIFIED from the getdata.infobelpro.com/Help portal: /api/token (password grant), POST /api/search,
// SearchInput (Website/BusinessName), Record fields businessName/webDomain/phone/employeesTotal/
// salesVolume/yearStarted/country/city/legalStatusCodeDescription/InfobelCategories. Field names/types
// pinned UNVERIFIED until a live authorized call (see hunter.go). First consumer of oauth2 password grant.
func InfobelPRO(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://getdata.infobelpro.com"
	}
	tokenURL := "https://getdata.infobelpro.com/api/token"
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		tokenURL = u.Scheme + "://" + u.Host + "/api/token"
	}
	return &provider.HTTPAdapter{
		NameV:   "infobelpro",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthOAuth2CC,
			KeyPoolSelector: "infobelpro:default",
			TokenURL:        tokenURL,
			TokenStyle:      "password",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]any{"returnFirstPage": true}
			if d := req.Known[domain.FieldCompanyDomain]; d != "" {
				body["Website"] = []string{d}
			}
			if n := req.Known[domain.FieldCompanyName]; n != "" {
				body["BusinessName"] = []string{n}
			}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/search", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				FirstPageRecords []struct {
					BusinessName      string          `json:"businessName"`
					WebDomain         string          `json:"webDomain"`
					Phone             string          `json:"phone"`
					EmployeesTotal    json.RawMessage `json:"employeesTotal"`
					SalesVolume       json.RawMessage `json:"salesVolume"`
					YearStarted       json.RawMessage `json:"yearStarted"`
					Country           string          `json:"country"`
					City              string          `json:"city"`
					LegalStatusDesc   string          `json:"legalStatusCodeDescription"`
					InfobelCategories []struct {
						Label01 string `json:"infobelLabel01"`
					} `json:"InfobelCategories"`
				} `json:"FirstPageRecords"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.FirstPageRecords) == 0 {
				return res, nil
			}
			r := p.FirstPageRecords[0]
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, r.BusinessName, 0.85)
			put(domain.FieldCompanyDomain, bareDomain(r.WebDomain), 0.80)
			put(domain.FieldCompanyPhone, r.Phone, 0.75)
			put(domain.FieldEmployeeCount, rawStr(r.EmployeesTotal), 0.70)
			put(domain.FieldCompanyRevenue, rawStr(r.SalesVolume), 0.65)
			put(domain.FieldCompanyFoundedYear, yearOf(rawStr(r.YearStarted)), 0.75)
			put(domain.FieldCompanyHQCountry, r.Country, 0.85)
			put(domain.FieldCompanyHQCity, r.City, 0.80)
			put(domain.FieldCompanyType, r.LegalStatusDesc, 0.80)
			if len(r.InfobelCategories) > 0 {
				put(domain.FieldIndustry, r.InfobelCategories[0].Label01, 0.65)
			}
			return res, nil
		},
	}
}
