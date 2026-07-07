package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// mixrankKeyPlaceholder is the letters-only path sentinel MixRank's Build writes where the API key
// belongs; the egress AuthInjector (AuthAPIKeyPath, ADR-0024 Phase 4) substitutes the leased key.
const mixrankKeyPlaceholder = "MIXRANKAPIKEY"

// MixRank builds an adapter for the MixRank Companies Match API (docs/03 §7).
//   - Endpoint: GET {base}/{apiKey}/companies/match?url=&name=&linkedin=  (base default
//     https://api.mixrank.com/v2/json) [mixrank.com/api/documentation].
//   - Auth: the API key is a URL PATH SEGMENT (not header/query) — AuthAPIKeyPath: Build writes the
//     MIXRANKAPIKEY sentinel, the egress injector replaces it with the leased key. Adapter holds none.
//   - Input: company_domain (url) / company_name / company_linkedin_url. Fills firmographics from the
//     flat company object.
//   - Status: DEPRIORITIZED (ADR-0009) — LinkedIn + web-crawl + app-store provenance.
//
// VERIFIED from docs: path-segment key, /companies/match params, company field names (name,
// employees, industries[]/sic[]/naics[] of {id,name}, linkedin.{url,website,type,founded}, address.*).
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go). This is the first
// consumer of AuthAPIKeyPath.
func MixRank(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.mixrank.com/v2/json"
	}
	return &provider.HTTPAdapter{
		NameV:   "mixrank",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyPath,
			PathPlaceholder: mixrankKeyPlaceholder,
			KeyPoolSelector: "mixrank:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldNAICS, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/" + mixrankKeyPlaceholder + "/companies/match")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "url", req.Known[domain.FieldCompanyDomain])
			setIf(q, "name", req.Known[domain.FieldCompanyName])
			setIf(q, "linkedin", req.Known[domain.FieldCompanyLinkedInURL])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Name       string `json:"name"`
				Employees  int64  `json:"employees"`
				Industries []struct {
					Name string `json:"name"`
				} `json:"industries"`
				SIC []struct {
					ID string `json:"id"`
				} `json:"sic"`
				NAICS []struct {
					ID string `json:"id"`
				} `json:"naics"`
				LinkedIn struct {
					URL     string `json:"url"`
					Website string `json:"website"`
					Type    string `json:"type"`
					Founded int64  `json:"founded"`
				} `json:"linkedin"`
				Address struct {
					City    string `json:"city"`
					Country string `json:"country"`
				} `json:"address"`
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
			put(domain.FieldCompanyName, p.Name, 0.65)
			put(domain.FieldCompanyDomain, p.LinkedIn.Website, 0.60)
			put(domain.FieldCompanyType, p.LinkedIn.Type, 0.60)
			put(domain.FieldCompanyLinkedInURL, p.LinkedIn.URL, 0.70)
			put(domain.FieldCompanyHQCountry, p.Address.Country, 0.65)
			put(domain.FieldCompanyHQCity, p.Address.City, 0.65)
			if p.Employees > 0 {
				put(domain.FieldEmployeeCount, itoa(p.Employees), 0.65)
			}
			if p.LinkedIn.Founded > 0 {
				put(domain.FieldCompanyFoundedYear, itoa(p.LinkedIn.Founded), 0.65)
			}
			if len(p.Industries) > 0 {
				put(domain.FieldIndustry, p.Industries[0].Name, 0.65)
			}
			if len(p.SIC) > 0 {
				put(domain.FieldSIC, p.SIC[0].ID, 0.60)
			}
			if len(p.NAICS) > 0 {
				put(domain.FieldNAICS, p.NAICS[0].ID, 0.60)
			}
			return res, nil
		},
	}
}
