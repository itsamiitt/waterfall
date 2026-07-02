// Package adapters holds concrete, API-first provider adapters built on
// provider.HTTPAdapter. Each targets a real vendor from docs/03.
//
// HONESTY NOTE (per the no-fabrication rule): the auth SCHEME and error-status handling
// follow docs/03 + skills/api-integration and are the load-bearing contract. The exact
// request/response FIELD NAMES are REPRESENTATIVE of each vendor's documented shape and
// are marked `UNVERIFIED` — they MUST be confirmed against the vendor's live API/OpenAPI
// before production. The assumed shapes are pinned as fixtures in `testdata/` (see
// `testdata/README_UNVERIFIED.md`) and exercised end-to-end by `live_smoke_test.go` (through
// the egress key-injection seam and the HTTP status->error-class mapping), so a shape change
// is a visible, tested diff. Adapters are structured so that confirming the shape is a
// localized change to Build/Decode only.
package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Hunter builds an adapter for Hunter.io's Email Finder (docs/03).
//   - Endpoint: GET {base}?domain=&first_name=&last_name=  (base default
//     https://api.hunter.io/v2/email-finder)
//   - Auth: api_key as a QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Quirk: 403 is used as a throttle signal -> ClassRateLimit (skills/api-integration).
//   - Fills: work_email (confidence = score/100) and email_status (from verification).
func Hunter(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.hunter.io/v2/email-finder"
	}
	return &provider.HTTPAdapter{
		NameV:   "hunter",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api_key",
			KeyPoolSelector: "hunter:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 10, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 10, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("domain", req.Known[domain.FieldCompanyDomain])
			q.Set("first_name", req.Known[domain.FieldFirstName])
			q.Set("last_name", req.Known[domain.FieldLastName])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: decodeHunter,
	}
}

func decodeHunter(body []byte) (provider.Result, error) {
	var p struct {
		Data struct {
			Email        string `json:"email"`
			Score        int    `json:"score"` // 0..100
			Verification struct {
				Status string `json:"status"` // "valid" | "invalid" | "accept_all" | ...
			} `json:"verification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if p.Data.Email != "" {
		res.Values[domain.FieldWorkEmail] = provider.Observation{
			Value:      p.Data.Email,
			Confidence: domain.Confidence(float64(p.Data.Score) / 100.0).Clamp(),
		}
	}
	if p.Data.Verification.Status != "" {
		res.Values[domain.FieldEmailStatus] = provider.Observation{
			Value:      p.Data.Verification.Status,
			Confidence: 0.90,
		}
	}
	return res, nil
}
