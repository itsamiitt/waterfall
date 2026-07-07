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

// Telnyx builds an adapter for the Telnyx Number Lookup API (docs/03 §7) — carrier/LRN line-type.
//   - Endpoint: GET {base}/{e164}?type=carrier,caller-name  (base default
//     https://api.telnyx.com/v2/number_lookup) [developers.telnyx.com/docs/identity/number-lookup].
//   - Auth: Bearer v2 API key, injected at egress (AuthBearer).
//   - Input: mobile_phone. Fills: phone_status (from data.carrier.type) + echoed mobile_phone.
//   - Quirk: no valid/invalid boolean — data.carrier.error_code (non-null) or empty carrier name
//     means unresolved -> "unknown"; caller_name.error_code is a CNAM sub-status and is ignored.
//
// VERIFIED from docs/OpenAPI: endpoint, Bearer auth, data.carrier.{type,error_code,name}. Full
// line-type enum + query encoding pinned UNVERIFIED until a live authorized call (see hunter.go).
func Telnyx(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.telnyx.com/v2/number_lookup"
	}
	return &provider.HTTPAdapter{
		NameV:   "telnyx",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "telnyx:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(req.Known[domain.FieldMobilePhone])
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			q := r.URL.Query()
			q.Set("type", "carrier,caller-name")
			r.URL.RawQuery = q.Encode()
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					PhoneNumber string `json:"phone_number"`
					Carrier     struct {
						Type      string `json:"type"`
						Name      string `json:"name"`
						ErrorCode string `json:"error_code"`
					} `json:"carrier"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			status := "unknown"
			if p.Data.Carrier.ErrorCode == "" && p.Data.Carrier.Name != "" {
				status = phoneStatusFromType(p.Data.Carrier.Type)
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.90}
			if p.Data.PhoneNumber != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.Data.PhoneNumber, Confidence: 0.95}
			}
			return res, nil
		},
	}
}
