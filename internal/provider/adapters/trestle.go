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

// Trestle builds an adapter for the Trestle Phone Validation API (docs/03 §8).
//   - Endpoint: GET {base}?phone=  (base default https://api.trestleiq.com/3.0/phone_intel)
//     [docs.trestleiq.com/api-reference/phone-validation-api].
//   - Auth: API key in the "x-api-key" HEADER, injected at egress (AuthAPIKeyHeader).
//   - Input: mobile_phone. Fills: phone_status — derived from is_valid + line_type (Landline|Mobile|
//     FixedVOIP|NonFixedVOIP|Premium|TollFree|Voicemail|Other) into the normalized
//     valid_mobile|valid_landline|valid_voip|valid_other|invalid vocabulary.
//   - Quirk: 200-with-error-body — partial success returns HTTP 200 with an "error" object
//     {name:"InternalError",message:"Could not retrieve entire response"} (external timeout) →
//     TRANSIENT (retryable). Soft input problems come as a "warnings" array in a 200 body; with no
//     verdict Decode omits phone_status (NOT_FOUND). Non-2xx: 403 auth/product-access, 429 rate limit.
//
// VERIFIED from docs: endpoint, x-api-key header, is_valid/line_type fields, the 200 error/warnings
// quirk. Exact JSON field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Trestle(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.trestleiq.com/3.0/phone_intel"
	}
	return &provider.HTTPAdapter{
		NameV:   "trestle",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "x-api-key",
			KeyPoolSelector: "trestle:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 2, ExpectedConfidence: 0.90},
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
				IsValid  *bool    `json:"is_valid"`
				LineType string   `json:"line_type"`
				Warnings []string `json:"warnings"`
				Err      struct {
					Name    string `json:"name"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body: external timeout / could-not-complete → transient (retryable).
			if p.Err.Name != "" {
				msg := p.Err.Message
				if msg == "" {
					msg = p.Err.Name
				}
				return provider.Result{}, domain.NewProviderError("trestle", domain.ClassTransient, errors.New(msg))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.IsValid != nil {
				var status string
				lt := strings.ToLower(p.LineType)
				switch {
				case !*p.IsValid:
					status = "invalid"
				case strings.Contains(lt, "voip"):
					status = "valid_voip"
				default:
					status = phoneStatusFromType(p.LineType)
				}
				res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
