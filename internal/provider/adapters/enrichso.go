package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// EnrichSo builds an adapter for the Enrich.so v3 reverse-lookup (email → profile) API (docs/03 §1).
//   - Endpoint: POST {base} with JSON {"email":"…"}  (base default
//     https://dev.enrich.so/api/v3/reverse-lookup/lookup — the current v3 host is literally
//     "dev.enrich.so", verified live) [doc.enrich.so/look-up-a-professional-profile-by-email].
//   - Auth: API key in the "x-api-key" HEADER, injected at egress (AuthAPIKeyHeader).
//   - Input: work_email. Fills full_name/first_name/last_name, linkedin_url (profileUrl), company_name,
//     job_title (positions.positionHistory[0].title).
//   - Status: DEPRIORITIZED (ADR-0009) — the success body is a scraped LinkedIn profile
//     (media.licdn.com photo, /in/ profileUrl, LinkedIn member-URN id), so it needs a compliance
//     review before it can serve. Quirk: 402 = insufficient credits (QUOTA), not charged on no-match;
//     RFC7807 problem+json error bodies.
//
// VERIFIED (live 2026-07-07) from docs: POST endpoint, x-api-key header, data.* field names. Exact
// no-match body shape pinned UNVERIFIED until a live authorized call (see hunter.go).
func EnrichSo(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://dev.enrich.so/api/v3/reverse-lookup/lookup"
	}
	return &provider.HTTPAdapter{
		NameV:   "enrich-so",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "x-api-key",
			KeyPoolSelector: "enrich-so:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldFullName, Cost: 10, ExpectedConfidence: 0.85},
			{Field: domain.FieldFirstName, Cost: 10, ExpectedConfidence: 0.85},
			{Field: domain.FieldLastName, Cost: 10, ExpectedConfidence: 0.85},
			{Field: domain.FieldLinkedInURL, Cost: 10, ExpectedConfidence: 0.85},
			{Field: domain.FieldJobTitle, Cost: 10, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyName, Cost: 10, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload, err := json.Marshal(map[string]string{"email": req.Known[domain.FieldWorkEmail]})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					DisplayName string `json:"displayName"`
					FirstName   string `json:"firstName"`
					LastName    string `json:"lastName"`
					ProfileURL  string `json:"profileUrl"`
					CompanyName string `json:"companyName"`
					Positions   struct {
						PositionHistory []struct {
							Title string `json:"title"`
						} `json:"positionHistory"`
					} `json:"positions"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, conf float64) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: domain.Confidence(conf).Clamp()}
				}
			}
			put(domain.FieldFullName, p.Data.DisplayName, 0.85)
			put(domain.FieldFirstName, p.Data.FirstName, 0.85)
			put(domain.FieldLastName, p.Data.LastName, 0.85)
			put(domain.FieldLinkedInURL, p.Data.ProfileURL, 0.85)
			put(domain.FieldCompanyName, p.Data.CompanyName, 0.80)
			if len(p.Data.Positions.PositionHistory) > 0 {
				put(domain.FieldJobTitle, p.Data.Positions.PositionHistory[0].Title, 0.80)
			}
			return res, nil
		},
	}
}
