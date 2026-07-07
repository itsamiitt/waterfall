package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Veriphone builds an adapter for the Veriphone v2 verify API (docs/03 §7).
//   - Endpoint: GET {base}?phone=  (base default https://api.veriphone.io/v2/verify)
//     [veriphone.io/docs/v2].
//   - Auth: Bearer token (recommended over the ?key= query form), injected at egress (AuthBearer).
//   - Input: mobile_phone. Fills: phone_status (phone_valid + phone_type: mobile/fixed-line/VoIP/
//     toll-free) + echoed E.164 (`e164`).
//
// VERIFIED from docs: endpoint, Bearer auth, phone_valid/phone_type/carrier/e164. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func Veriphone(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.veriphone.io/v2/verify"
	}
	return &provider.HTTPAdapter{
		NameV:   "veriphone",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "veriphone:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("phone", req.Known[domain.FieldMobilePhone])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				PhoneValid bool   `json:"phone_valid"`
				PhoneType  string `json:"phone_type"`
				E164       string `json:"e164"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			status := "invalid"
			if p.PhoneValid {
				status = phoneStatusFromType(p.PhoneType)
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.80}
			if p.E164 != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.E164, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
