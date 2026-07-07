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

// Diffbot builds an adapter for the Diffbot Knowledge Graph Enhance API (Organization) (docs/03 §7).
//   - Endpoint: GET {base}?type=Organization&url=&name=&location=  (base default
//     https://kg.diffbot.com/kg/v3/enhance) [docs.diffbot.com/reference/enhanceget].
//   - Auth: token in the "token" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: company_domain (url), company_name (name), company_hq_country (location). Fills
//     firmographics from data[0].entity. No match = 200 with empty data[] (hits:0).
//   - Quirk: 429 = insufficient credits (QUOTA), not rate-limit — the shared map treats 429 as
//     RATE_LIMIT (retry/backoff); acceptable, noted UNVERIFIED.
//
// VERIFIED from docs: endpoint, token query auth, {data:[{entity}]} wrapper, entity fields
// name/homepageUri/nbEmployees/revenue.value/foundingDate.str/linkedInUri/naicsClassification/
// sicClassification. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Diffbot(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://kg.diffbot.com/kg/v3/enhance"
	}
	return &provider.HTTPAdapter{
		NameV:   "diffbot",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "token",
			KeyPoolSelector: "diffbot:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.55},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldNAICS, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("type", "Organization")
			setIf(q, "url", req.Known[domain.FieldCompanyDomain])
			setIf(q, "name", req.Known[domain.FieldCompanyName])
			setIf(q, "location", req.Known[domain.FieldCompanyHQCountry])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data []struct {
					Entity struct {
						Name        string `json:"name"`
						HomepageURI string `json:"homepageUri"`
						LinkedInURI string `json:"linkedInUri"`
						NbEmployees int64  `json:"nbEmployees"`
						Revenue     struct {
							Value int64 `json:"value"`
						} `json:"revenue"`
						FoundingDate struct {
							Str string `json:"str"`
						} `json:"foundingDate"`
						NAICS []struct {
							Code string `json:"code"`
						} `json:"naicsClassification"`
						SIC []struct {
							Code string `json:"code"`
						} `json:"sicClassification"`
					} `json:"entity"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Data) == 0 {
				return res, nil
			}
			e := p.Data[0].Entity
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, e.Name, 0.65)
			put(domain.FieldCompanyDomain, bareDomain(e.HomepageURI), 0.70)
			put(domain.FieldCompanyLinkedInURL, e.LinkedInURI, 0.70)
			// foundingDate.str is a DDate like "d1911-06-01" — strip the leading 'd', take the year.
			put(domain.FieldCompanyFoundedYear, yearOf(strings.TrimPrefix(e.FoundingDate.Str, "d")), 0.65)
			if e.NbEmployees > 0 {
				put(domain.FieldEmployeeCount, itoa(e.NbEmployees), 0.60)
			}
			if e.Revenue.Value > 0 {
				put(domain.FieldCompanyRevenue, itoa(e.Revenue.Value), 0.55)
			}
			if len(e.NAICS) > 0 {
				put(domain.FieldNAICS, e.NAICS[0].Code, 0.65)
			}
			if len(e.SIC) > 0 {
				put(domain.FieldSIC, e.SIC[0].Code, 0.65)
			}
			return res, nil
		},
	}
}
