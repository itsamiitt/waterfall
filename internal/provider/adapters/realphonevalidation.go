package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// RealPhoneValidation builds an adapter for the RealPhoneValidation Turbo v3 API (docs/03 §7) —
// US phone line-type + connection validation.
//   - Endpoint: GET {base}TurboV3.php?phone={10 digits}&output=json  (base default
//     https://api.realvalidation.com/rpvWebService/) [realphonevalidation.com/api-documentation].
//     NOTE: marketing domain is realphonevalidation.com but the live host is api.realvalidation.com.
//   - Auth: token in the "token" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: mobile_phone (stripped to its last 10 digits). Fills phone_status from status+phone_type.
//   - output=json is REQUIRED (the API defaults to XML).
//
// VERIFIED from docs: host/endpoint, token query auth, status + phone_type + carrier fields, the
// status/phone_type value sets. The phone_status derivation is our mapping (UNVERIFIED as
// vendor-stated). Operational states (unauthorized/restricted/server-unavailable/ERROR) surface as a
// 200-with-in-body state → classified as AUTH/TRANSIENT so the engine reacts (see hunter.go honesty).
func RealPhoneValidation(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.realvalidation.com/rpvWebService/"
	}
	return &provider.HTTPAdapter{
		NameV:   "realphonevalidation",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyQuery, QueryParam: "token", KeyPoolSelector: "realphonevalidation:default"},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 2, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/TurboV3.php")
			if err != nil {
				return nil, err
			}
			d := digitsOnly(req.Known[domain.FieldMobilePhone])
			if len(d) > 10 {
				d = d[len(d)-10:] // RealPhoneValidation wants the 10-digit US number (drop country code)
			}
			q := u.Query()
			q.Set("phone", d)
			q.Set("output", "json")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Status    string `json:"status"`
				PhoneType string `json:"phone_type"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			switch p.Status {
			case "unauthorized", "restricted":
				return provider.Result{}, domain.NewProviderError("realphonevalidation", domain.ClassAuth, errors.New(p.Status))
			case "server-unavailable", "ERROR", "":
				if p.Status != "" {
					return provider.Result{}, domain.NewProviderError("realphonevalidation", domain.ClassTransient, errors.New(p.Status))
				}
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: rpvPhoneStatus(p.Status, p.PhoneType), Confidence: 0.90}
			return res, nil
		},
	}
}

// rpvPhoneStatus maps a RealPhoneValidation (status, phone_type) pair onto the canonical phone_status
// vocabulary. connected/connected-75 with a known line type → valid_<type>; pending/unknown-type →
// valid_unknown; everything else (disconnected*, invalid-*, busy, unreachable) → invalid.
func rpvPhoneStatus(status, phoneType string) string {
	switch status {
	case "connected", "connected-75":
		switch phoneType {
		case "Mobile":
			return "valid_mobile"
		case "Landline":
			return "valid_landline"
		case "VoIP":
			return "valid_voip"
		default:
			return "valid_unknown"
		}
	case "pending":
		return "valid_unknown"
	default:
		return "invalid"
	}
}
