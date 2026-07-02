package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Prospeo builds an adapter for Prospeo's Email Finder (docs/03).
//   - Endpoint: POST {base} with JSON {first_name,last_name,company} (base default
//     https://api.prospeo.io/email-finder).
//   - Auth: API key in the "X-KEY" header, injected at egress (AuthAPIKeyHeader).
//   - Quirk: 402 -> credits exhausted -> ClassQuota (skills/api-integration).
//   - Fills: work_email + email_status.
//
// Wire format is REPRESENTATIVE / `UNVERIFIED` — confirm against live docs (see hunter.go).
func Prospeo(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.prospeo.io/email-finder"
	}
	return &provider.HTTPAdapter{
		NameV:   "prospeo",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-KEY",
			KeyPoolSelector: "prospeo:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 8, ExpectedConfidence: 0.82},
			{Field: domain.FieldEmailStatus, Cost: 8, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload, err := json.Marshal(map[string]string{
				"first_name": req.Known[domain.FieldFirstName],
				"last_name":  req.Known[domain.FieldLastName],
				"company":    req.Known[domain.FieldCompanyDomain],
			})
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
		Decode: decodeProspeo,
	}
}

func decodeProspeo(body []byte) (provider.Result, error) {
	var p struct {
		Error    bool `json:"error"`
		Response struct {
			Email       string `json:"email"`
			EmailStatus string `json:"email_status"` // "valid" | "catch_all" | ...
			EmailScore  int    `json:"email_score"`  // 0..100 (representative)
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if p.Response.Email != "" {
		conf := domain.Confidence(0.82)
		if p.Response.EmailScore > 0 {
			conf = domain.Confidence(float64(p.Response.EmailScore) / 100.0).Clamp()
		}
		res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Response.Email, Confidence: conf}
	}
	if p.Response.EmailStatus != "" {
		res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Response.EmailStatus, Confidence: 0.85}
	}
	return res, nil
}
