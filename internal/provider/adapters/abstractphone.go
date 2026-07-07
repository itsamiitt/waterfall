package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// AbstractPhone builds an adapter for the AbstractAPI Phone Validation API (docs/03 §7).
//   - Endpoint: GET {base}?phone=  (base default https://phonevalidation.abstractapi.com/v1/)
//     [docs.abstractapi.com/api/phone-validation].
//   - Auth: API key in the "api_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: mobile_phone. Fills: phone_status (valid + `type`) + echoed E.164 (`format.international`).
//
// VERIFIED from docs: endpoint, api_key auth, top-level `valid` + `type` (Mobile/Landline/…),
// nested `format.international`, top-level `carrier`. Field names pinned UNVERIFIED until a live
// authorized call (see hunter.go).
func AbstractPhone(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://phonevalidation.abstractapi.com/v1/"
	}
	return &provider.HTTPAdapter{
		NameV:   "abstract-phone",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "api_key",
			KeyPoolSelector: "abstract-phone:default",
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
				Valid  bool   `json:"valid"`
				Type   string `json:"type"`
				Format struct {
					International string `json:"international"`
				} `json:"format"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			status := "invalid"
			if p.Valid {
				status = phoneStatusFromType(p.Type)
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.80}
			if p.Format.International != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.Format.International, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
