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

// NeutrinoAPI builds an adapter for the NeutrinoAPI Phone Validate endpoint (docs/03 §8).
//   - Endpoint: POST {base}/phone-validate with form param number (base default https://neutrinoapi.net)
//     [neutrinoapi.com/api/phone-validate].
//   - Auth: DUAL credential headers "User-ID" + "API-Key" (ADR-0024 Phase 4b, AuthAPIKeyDualHeader) —
//     the "<slug>:default" pool secret holds "<user-id>:<api-key>"; egress splits on the first colon.
//   - Input: mobile_phone. Fills: phone_status (valid + type: mobile|fixed-line|premium-rate|toll-free|
//     voip|unknown → normalized) + mobile_phone (the E.164 international-number).
//   - Quirk: response keys are KEBAB-CASE (default output-case). Failures use real non-200 codes with
//     an in-body integer api-error + api-error-msg (403 = bad credentials [43] or daily limit [2],
//     500 = fatal [51]). Validation is number-plan based, not a live HLR lookup.
//
// VERIFIED from docs: endpoint, dual-header auth, kebab-case fields, api-error codes. Exact field
// names pinned UNVERIFIED until a live authorized call (see hunter.go).
func NeutrinoAPI(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://neutrinoapi.net"
	}
	return &provider.HTTPAdapter{
		NameV:   "neutrinoapi",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:           provider.AuthAPIKeyDualHeader,
			HeaderName:       "User-ID",
			SecondHeaderName: "API-Key",
			KeyPoolSelector:  "neutrinoapi:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			form := url.Values{}
			form.Set("number", req.Known[domain.FieldMobilePhone])
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/phone-validate", strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Valid               *bool  `json:"valid"`
				Type                string `json:"type"`
				InternationalNumber string `json:"international-number"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Valid != nil {
				status := "invalid"
				if *p.Valid {
					status = phoneStatusFromType(p.Type)
				}
				res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.90}
			}
			if p.InternationalNumber != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.InternationalNumber, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
