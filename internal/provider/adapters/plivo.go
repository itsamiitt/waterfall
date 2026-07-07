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

// Plivo builds an adapter for the Plivo Number Lookup API (docs/03 §7).
//   - Endpoint: GET {base}/{e164}?type=carrier  (base default https://lookup.plivo.com/v1/Number)
//     [plivo.com/docs/lookup].
//   - Auth: HTTP Basic (Auth ID:Auth Token) — key-pool secret stored as "authid:authtoken",
//     injected at egress (AuthBasic).
//   - Input: mobile_phone. Fills: phone_status from carrier.type (mobile/fixed/voip/toll-free) +
//     echoed E.164. HTTP 404 = invalid/non-existent number -> NOT_FOUND (not billed).
//
// VERIFIED from docs + plivo-go SDK: endpoint, Basic auth, carrier.type enum, format.e164. Field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Plivo(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://lookup.plivo.com/v1/Number"
	}
	return &provider.HTTPAdapter{
		NameV:   "plivo",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBasic,
			KeyPoolSelector: "plivo:default", // secret stored as "authid:authtoken"
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
			q.Set("type", "carrier")
			r.URL.RawQuery = q.Encode()
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Carrier struct {
					Type string `json:"type"`
				} `json:"carrier"`
				Format struct {
					E164 string `json:"e164"`
				} `json:"format"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: phoneStatusFromType(p.Carrier.Type), Confidence: 0.90}
			if p.Format.E164 != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.Format.E164, Confidence: 0.95}
			}
			return res, nil
		},
	}
}
