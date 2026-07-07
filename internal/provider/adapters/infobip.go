package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Infobip builds an adapter for the Infobip Number Lookup (HLR) API (docs/03 §7).
//   - Endpoint: POST {base} with JSON {"to":[e164]}  (base default https://api.infobip.com/number/1/query;
//     each account has a personalized base host to override) [infobip.com/docs/number-lookup].
//   - Auth: API key header "Authorization: App <key>" — key-pool secret stored WITH the "App "
//     prefix, injected at egress (AuthAPIKeyHeader on "Authorization").
//   - Input: mobile_phone. Fills: phone_status derived from results[0].status.groupName + error.name
//     (HLR is mobile-only): DELIVERED+NO_ERROR→valid, EC_UNKNOWN_SUBSCRIBER→invalid,
//     UNDELIVERABLE→unreachable, else unknown.
//
// VERIFIED from docs: endpoint, App-key auth, results[].status/error schema. Field names + full
// error-name enum pinned UNVERIFIED until a live authorized call (see hunter.go).
func Infobip(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.infobip.com/number/1/query"
	}
	return &provider.HTTPAdapter{
		NameV:   "infobip",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization",
			KeyPoolSelector: "infobip:default", // secret stored as "App <key>"
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload := map[string][]string{"to": {req.Known[domain.FieldMobilePhone]}}
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Results []struct {
					Status struct {
						GroupName string `json:"groupName"`
					} `json:"status"`
					Error struct {
						Name string `json:"name"`
					} `json:"error"`
				} `json:"results"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Results) == 0 {
				return res, nil
			}
			r := p.Results[0]
			status := "unknown"
			switch {
			case r.Status.GroupName == "DELIVERED" && (r.Error.Name == "NO_ERROR" || r.Error.Name == ""):
				status = "valid_mobile"
			case r.Error.Name == "EC_UNKNOWN_SUBSCRIBER":
				status = "invalid"
			case r.Status.GroupName == "UNDELIVERABLE":
				status = "unreachable"
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.90}
			return res, nil
		},
	}
}
