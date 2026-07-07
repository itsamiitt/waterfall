package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// NumLookupAPI builds an adapter for the NumLookupAPI phone validation API (docs/03 §8).
//   - Endpoint: GET {base}/{number}  (base default https://api.numlookupapi.com/v1/validate); the
//     number is a URL PATH segment [numlookupapi.com/docs/validate].
//   - Auth: API key in the "apikey" HEADER (docs-recommended over the query form, which can leak the
//     key into access logs), injected at egress (AuthAPIKeyHeader).
//   - Input: mobile_phone. Fills: phone_status (from valid + line_type: landline|mobile|satellite|
//     paging|special_services|premium_rate|toll_free|N/A) + mobile_phone (the E.164 international_format).
//   - Quirk: an invalid/unroutable number is NOT an HTTP error — HTTP 200 with {"valid":false,…};
//     Decode reads the valid flag, not the status code. 422 validation error, 403 plan-required (QUOTA),
//     429 rate/quota. No documented JSON error-body schema (UNVERIFIED).
//
// VERIFIED from docs: endpoint (number in path), apikey header, valid/line_type/international_format
// fields, the 200-valid:false quirk. Exact field names pinned UNVERIFIED until a live key (see hunter.go).
func NumLookupAPI(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.numlookupapi.com/v1/validate"
	}
	return &provider.HTTPAdapter{
		NameV:   "numlookupapi",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "apikey",
			KeyPoolSelector: "numlookupapi:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			number := digitsOnly(req.Known[domain.FieldMobilePhone])
			u := strings.TrimRight(base, "/") + "/" + number
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Valid               *bool  `json:"valid"`
				LineType            string `json:"line_type"`
				InternationalFormat string `json:"international_format"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Valid != nil {
				status := "invalid"
				if *p.Valid {
					status = phoneStatusFromType(p.LineType)
				}
				res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.90}
			}
			if p.InternationalFormat != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.InternationalFormat, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
