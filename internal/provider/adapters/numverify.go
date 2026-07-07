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

// NumVerify builds an adapter for the NumVerify (apilayer) phone validation API (docs/03 §7).
//   - Endpoint: GET {base}?number=  (base default https://apilayer.net/api/validate)
//     [docs.apilayer.com/numverify].
//   - Auth: API key in the "access_key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: mobile_phone. Fills: phone_status (valid + line_type) + echoed E.164
//     (`international_format`).
//   - Quirk: errors come as HTTP 200 with `{"success":false,"error":{"type":..,"info":..}}` — Decode
//     classifies error.info via classifyErrMsg. valid:false is a normal "invalid".
//
// VERIFIED from docs: endpoint, access_key auth, valid/line_type/carrier/international_format,
// error object shape. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func NumVerify(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://apilayer.net/api/validate"
	}
	return &provider.HTTPAdapter{
		NameV:   "numverify",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "access_key",
			KeyPoolSelector: "numverify:default",
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
			q.Set("number", req.Known[domain.FieldMobilePhone])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Success             *bool  `json:"success"`
				Valid               bool   `json:"valid"`
				LineType            string `json:"line_type"`
				InternationalFormat string `json:"international_format"`
				Error               struct {
					Type string `json:"type"`
					Info string `json:"info"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			if p.Success != nil && !*p.Success {
				msg := p.Error.Type + " " + p.Error.Info
				return provider.Result{}, domain.NewProviderError("numverify", classifyErrMsg(msg), errors.New(msg))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			status := "invalid"
			if p.Valid {
				status = phoneStatusFromType(p.LineType)
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.80}
			if p.InternationalFormat != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.InternationalFormat, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
