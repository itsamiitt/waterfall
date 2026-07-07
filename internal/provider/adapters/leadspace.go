package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Leadspace builds an adapter for the Leadspace Single Enrichment API v3 (docs/03 §7).
//   - Endpoint: POST {base} with JSON {"company":{website,name}}  (base default
//     https://apigw.leadspace.com/enrichment/enrich/single) [support.leadspace.com].
//   - Auth: Bearer API key, injected at egress (AuthBearer).
//   - Input: company_domain (website) / company_name. Fills firmographics from data.company.* +
//     detected technographics. No match = 200 with data.enrichment_status "Not Enriched".
//   - Status: DEPRIORITIZED (ADR-0009) — AI data-graph + LinkedIn/web provenance. 427 = out of
//     credits (non-standard; the shared map treats it as TRANSIENT — noted UNVERIFIED).
//
// VERIFIED from docs: endpoint, Bearer auth, {company:…} request, data.company.* response fields.
// Type-uncertain fields (naics/sic, intent) are omitted; field names pinned UNVERIFIED (hunter.go).
func Leadspace(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://apigw.leadspace.com/enrichment/enrich/single"
	}
	return &provider.HTTPAdapter{
		NameV:   "leadspace",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "leadspace:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyType, Cost: 5, ExpectedConfidence: 0.45},
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.60},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			company := map[string]string{}
			putIf(company, "website", req.Known[domain.FieldCompanyDomain])
			putIf(company, "name", req.Known[domain.FieldCompanyName])
			b, err := json.Marshal(map[string]any{"company": company})
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
				Data struct {
					Company struct {
						Name      string `json:"name"`
						Website   string `json:"website"`
						Industry  string `json:"industry"`
						Ownership string `json:"ownership"`
						Phone     string `json:"phone"`
						LinkedIn  string `json:"linkedin_profile"`
						Size      struct {
							Exact int64 `json:"exact"`
						} `json:"size"`
						Revenue struct {
							Exact int64 `json:"exact"`
						} `json:"revenue"`
						Address struct {
							Country string `json:"country"`
							City    string `json:"city"`
						} `json:"address"`
						Analytics struct {
							InstalledBaseTechnologies []string `json:"installed_base_technologies"`
						} `json:"analytics"`
					} `json:"company"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			c := p.Data.Company
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, conf domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: conf}
				}
			}
			put(domain.FieldCompanyName, c.Name, 0.65)
			put(domain.FieldCompanyDomain, c.Website, 0.60)
			put(domain.FieldIndustry, c.Industry, 0.65)
			put(domain.FieldCompanyType, c.Ownership, 0.45)
			put(domain.FieldCompanyPhone, c.Phone, 0.60)
			put(domain.FieldCompanyLinkedInURL, c.LinkedIn, 0.70)
			put(domain.FieldCompanyHQCountry, c.Address.Country, 0.65)
			put(domain.FieldCompanyHQCity, c.Address.City, 0.65)
			put(domain.FieldTechnographics, normList(c.Analytics.InstalledBaseTechnologies), 0.60)
			if c.Size.Exact > 0 {
				put(domain.FieldEmployeeCount, itoa(c.Size.Exact), 0.65)
			}
			if c.Revenue.Exact > 0 {
				put(domain.FieldCompanyRevenue, itoa(c.Revenue.Exact), 0.60)
			}
			return res, nil
		},
	}
}
