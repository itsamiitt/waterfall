package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Crunchbase builds an adapter for the Crunchbase v4 API (docs/03 §7) — firmographics + funding.
//   - Endpoint: POST {base} — the Search API (base default
//     https://api.crunchbase.com/api/v4/searches/organizations). The entity-lookup path needs a
//     Crunchbase permalink/UUID, NOT a domain, so we search by website_url to enrich from a domain
//     in ONE call (results inline under entities[].properties) [data.crunchbase.com/docs].
//   - Auth: user key in the "X-cb-user-key" header, injected at egress (AuthAPIKeyHeader).
//   - Input: company_domain. Fills: company_name, company_domain, LinkedIn, founded year, industry
//     (categories), company_type, funding_stage, company_phone.
//
// VERIFIED from docs: search endpoint, X-cb-user-key auth, predicate query shape, properties field
// names. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Crunchbase(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.crunchbase.com/api/v4/searches/organizations"
	}
	return &provider.HTTPAdapter{
		NameV:   "crunchbase",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-cb-user-key",
			KeyPoolSelector: "crunchbase:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyFoundedYear, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldFundingStage, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]any{
				"field_ids": []string{
					"name", "website_url", "linkedin", "founded_on", "categories",
					"company_type", "funding_stage", "phone_number",
				},
				"query": []map[string]any{{
					"type":        "predicate",
					"field_id":    "website_url",
					"operator_id": "contains",
					"values":      []string{req.Known[domain.FieldCompanyDomain]},
				}},
				"limit": 1,
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
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Entities []struct {
					Properties struct {
						Name         string `json:"name"`
						WebsiteURL   string `json:"website_url"`
						CompanyType  string `json:"company_type"`
						FundingStage string `json:"funding_stage"`
						PhoneNumber  string `json:"phone_number"`
						LinkedIn     struct {
							Value string `json:"value"`
						} `json:"linkedin"`
						FoundedOn struct {
							Value string `json:"value"`
						} `json:"founded_on"`
						Categories []struct {
							Value string `json:"value"`
						} `json:"categories"`
					} `json:"properties"`
				} `json:"entities"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Entities) == 0 {
				return res, nil
			}
			pr := p.Entities[0].Properties
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, pr.Name, 0.80)
			put(domain.FieldCompanyDomain, bareDomain(pr.WebsiteURL), 0.80)
			put(domain.FieldCompanyLinkedInURL, pr.LinkedIn.Value, 0.80)
			put(domain.FieldCompanyFoundedYear, yearOf(pr.FoundedOn.Value), 0.80)
			put(domain.FieldCompanyType, pr.CompanyType, 0.80)
			put(domain.FieldFundingStage, pr.FundingStage, 0.70)
			put(domain.FieldCompanyPhone, pr.PhoneNumber, 0.70)
			cats := make([]string, 0, len(pr.Categories))
			for _, c := range pr.Categories {
				cats = append(cats, c.Value)
			}
			put(domain.FieldIndustry, normList(cats), 0.80)
			return res, nil
		},
	}
}
