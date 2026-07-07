package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// G2 builds an adapter for the G2 Buyer Intent API (docs/03 §7) — first-party buyer-activity
// signals from G2.com.
//   - Endpoint: GET {base} with dimensions/measures + a company-domain filter (base default
//     https://data.g2.com/api/v2/buyer_intent, the product-agnostic variant so no partner
//     product-id path param is required) [data.g2.com/api/v2/docs].
//   - Auth: Account API Token as a Bearer token, injected at egress (AuthBearer).
//   - Input: company_domain. Fills: buying_signal (signal_type), intent_topics (category), plus the
//     resolved buyer-org firmographics (company_name, company_domain, industry, hq country,
//     employee_count) when requested via the dimensions param. Signals/topics across rows are
//     aggregated into one normalized comma-joined value each.
//   - No status quirk: no match = 200 with empty data[] (success-with-no-value).
//   - NOTE: the product-scoped endpoint additionally exposes company_intent_score; it needs a
//     partner product id and is out of scope for this generic adapter.
//
// VERIFIED from the official OpenAPI: host, endpoint, Bearer (AccountAPIToken) auth, JSON:API
// data[].attributes shape, dimension names. Field paths pinned UNVERIFIED until a live call.
func G2(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://data.g2.com/api/v2/buyer_intent"
	}
	return &provider.HTTPAdapter{
		NameV:   "g2",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "g2:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldBuyingSignal, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldIntentTopics, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.78},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("dimensions", "signal_type,category_name,company_name,company_domain,company_industry,company_country,company_employees")
			q.Set("measures", "total_activity")
			q.Set("dimension_filters[company_domain_eq]", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: decodeG2,
	}
}

func decodeG2(body []byte) (provider.Result, error) {
	var p struct {
		Data []struct {
			Attributes struct {
				SignalType       string `json:"signal_type"`
				CategoryName     string `json:"category_name"`
				CompanyName      string `json:"company_name"`
				CompanyDomain    string `json:"company_domain"`
				CompanyIndustry  string `json:"company_industry"`
				CompanyCountry   string `json:"company_country"`
				CompanyEmployees int64  `json:"company_employees"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if len(p.Data) == 0 {
		return res, nil
	}
	var signals, topics []string
	for _, d := range p.Data {
		signals = append(signals, d.Attributes.SignalType)
		topics = append(topics, d.Attributes.CategoryName)
	}
	if v := normList(signals); v != "" {
		res.Values[domain.FieldBuyingSignal] = provider.Observation{Value: v, Confidence: 0.85}
	}
	if v := normList(topics); v != "" {
		res.Values[domain.FieldIntentTopics] = provider.Observation{Value: v, Confidence: 0.75}
	}
	a := p.Data[0].Attributes
	put := func(f domain.Field, v string, c domain.Confidence) {
		if v != "" {
			res.Values[f] = provider.Observation{Value: v, Confidence: c}
		}
	}
	put(domain.FieldCompanyName, a.CompanyName, 0.75)
	put(domain.FieldCompanyDomain, a.CompanyDomain, 0.78)
	put(domain.FieldIndustry, a.CompanyIndustry, 0.70)
	put(domain.FieldCompanyHQCountry, a.CompanyCountry, 0.70)
	if a.CompanyEmployees > 0 {
		res.Values[domain.FieldEmployeeCount] = provider.Observation{Value: itoa(a.CompanyEmployees), Confidence: 0.70}
	}
	return res, nil
}
