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

// Telesign builds an adapter for the Telesign PhoneID API (docs/03 §7) — phone type + carrier.
//   - Endpoint: GET {base}/{phone}  (base default https://rest-ww.telesign.com/v1/phoneid)
//     [developer.telesign.com/enterprise/docs/phone-id-get-started].
//   - Auth: HTTP Basic (CustomerID:APIKey) — key-pool secret stored as "customerid:apikey",
//     injected at egress (AuthBasic).
//   - Input: mobile_phone. Fills: phone_status from phone_type.description
//     (MOBILE/FIXED_LINE/VOIP/…; INVALID → "invalid").
//
// VERIFIED from docs: endpoint, Basic (CustomerID/APIKey) auth, phone_type{code,description}. Field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Telesign(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://rest-ww.telesign.com/v1/phoneid"
	}
	return &provider.HTTPAdapter{
		NameV:   "telesign",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBasic,
			KeyPoolSelector: "telesign:default", // secret stored as "customerid:apikey"
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.85},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(req.Known[domain.FieldMobilePhone])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				PhoneType struct {
					Code        string `json:"code"`
					Description string `json:"description"`
				} `json:"phone_type"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			status := "unknown"
			switch {
			case strings.EqualFold(p.PhoneType.Description, "INVALID") || p.PhoneType.Code == "7":
				status = "invalid"
			case p.PhoneType.Description != "":
				status = phoneStatusFromType(p.PhoneType.Description)
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.85}
			return res, nil
		},
	}
}
