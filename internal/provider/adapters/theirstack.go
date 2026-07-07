package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// TheirStack builds an adapter for the TheirStack Technographics API (docs/03 §7) — backend tech
// detected from a company's public job postings.
//   - Endpoint: POST {base} with a JSON filter body (base default
//     https://api.theirstack.com/v1/companies/technologies) [theirstack.com/en/docs/api-reference].
//   - Auth: Bearer token, injected at egress (AuthBearer).
//   - Input: company_domain. Fills: technographics (sorted, comma-joined technology names).
//   - No special status quirk (402 -> QUOTA is the standard map); no match = 200 with empty data[].
//
// VERIFIED from the official OpenAPI: endpoint, Bearer auth, CompanyKeywordsResponse shape
// (data[].technology.name/slug, confidence low|medium|high). The exact request filter encoding is
// pinned UNVERIFIED until a live authorized call (see hunter.go).
func TheirStack(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.theirstack.com/v1/companies/technologies"
	}
	return &provider.HTTPAdapter{
		NameV:   "theirstack",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "theirstack:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldTechnographics, Cost: 3, ExpectedConfidence: 0.75},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload := map[string]any{
				"company_domain": []string{req.Known[domain.FieldCompanyDomain]},
				"limit":          25,
			}
			b, err := json.Marshal(payload)
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
				Data []struct {
					Technology struct {
						Name string `json:"name"`
					} `json:"technology"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			techs := make([]string, 0, len(p.Data))
			for _, d := range p.Data {
				techs = append(techs, d.Technology.Name)
			}
			if v := normList(techs); v != "" {
				res.Values[domain.FieldTechnographics] = provider.Observation{Value: v, Confidence: 0.75}
			}
			return res, nil
		},
	}
}
