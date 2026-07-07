package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Vonage builds an adapter for the Vonage Number Insight (Standard) API (docs/03 §7).
//   - Endpoint: GET {base}?number=  (base default https://api.nexmo.com/ni/standard/json)
//     [developer.vonage.com/en/api/number-insight].
//   - Auth: HTTP Basic (api_key:api_secret) — the key-pool secret must be stored as
//     "api_key:api_secret", injected at egress (AuthBasic). (Vonage also accepts the pair as
//     api_key/api_secret query params; Basic keeps the secret off the URL.)
//   - Input: mobile_phone. Fills: phone_status from current_carrier.network_type, gated by the
//     top-level `status` int (0 = success).
//   - Quirk: errors come as HTTP 200 with a non-zero `status` int — 4→AUTH, 9→QUOTA, 1→RATE_LIMIT
//     (classified so the engine acts correctly); other non-zero → no value.
//
// VERIFIED from docs: endpoint, Basic auth, `status` codes, current_carrier.network_type. Field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Vonage(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.nexmo.com/ni/standard/json"
	}
	return &provider.HTTPAdapter{
		NameV:   "vonage",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBasic,
			KeyPoolSelector: "vonage:default", // secret stored as "api_key:api_secret"
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("number", req.Known[domain.FieldMobilePhone])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Status         int `json:"status"`
				CurrentCarrier struct {
					NetworkType string `json:"network_type"`
				} `json:"current_carrier"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			switch p.Status {
			case 0: // success
			case 4:
				return provider.Result{}, domain.NewProviderError("vonage", domain.ClassAuth, errors.New("vonage status 4: invalid credentials"))
			case 9:
				return provider.Result{}, domain.NewProviderError("vonage", domain.ClassQuota, errors.New("vonage status 9: partner quota exceeded"))
			case 1:
				return provider.Result{}, domain.NewProviderError("vonage", domain.ClassRateLimit, errors.New("vonage status 1: throttled"))
			default:
				return provider.Result{Values: map[domain.Field]provider.Observation{}}, nil
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: phoneStatusFromType(p.CurrentCarrier.NetworkType), Confidence: 0.90}
			return res, nil
		},
	}
}
