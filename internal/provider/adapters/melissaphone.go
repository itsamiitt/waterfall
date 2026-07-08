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

// MelissaGlobalPhone builds an adapter for the Melissa Global Phone verification API (docs/03 §8).
//   - Endpoint: GET {base}/doGlobalPhone?phone=&ctry=  (base default
//     https://globalphone.melissadata.net/v4/WEB/GlobalPhone) [official OpenAPI spec,
//     MelissaData/MelissaCloudAPI-OpenAPI-Specifications].
//   - Auth: License Key in the "id" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: mobile_phone (+company_hq_country as the suspected-country hint). Fills: phone_status —
//     the per-record comma-delimited Results code string: PS01 = valid (with PS08 → landline);
//     absence of PS01 = not verified → invalid — and mobile_phone (InternationalPhoneNumber,
//     standardized dialable form; line type is NOT asserted in default Express mode, ~0.65).
//   - Quirk: request-level failures (incl. license-key problems) arrive INSIDE an HTTP 200 as a
//     non-empty top-level TransmissionResults string (specific GE-code values UNVERIFIED — any
//     non-empty value is treated as a request-level AUTH-class error). Invalid numbers are also
//     HTTP 200 (verdict in Records[].Results; no 404 no-match).
//
// VERIFIED from the vendor's official OpenAPI spec: endpoint, id query auth, Records field names,
// PS01/PS08 semantics. Full result-code table + field names pinned UNVERIFIED until a live key.
func MelissaGlobalPhone(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://globalphone.melissadata.net/v4/WEB/GlobalPhone"
	}
	return &provider.HTTPAdapter{
		NameV:   "melissa-global-phone",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "id",
			KeyPoolSelector: "melissa-global-phone:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 1, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/doGlobalPhone")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("phone", req.Known[domain.FieldMobilePhone])
			setIfQ(q, "ctry", req.Known[domain.FieldCompanyHQCountry])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				TransmissionResults string `json:"TransmissionResults"`
				Records             []struct {
					Results                  string `json:"Results"`
					InternationalPhoneNumber string `json:"InternationalPhoneNumber"`
				} `json:"Records"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// Request-level error inside HTTP 200 (license key etc.) — any non-empty value.
			if strings.TrimSpace(p.TransmissionResults) != "" {
				return provider.Result{}, domain.NewProviderError("melissa-global-phone", domain.ClassAuth,
					errors.New("transmission error codes: "+p.TransmissionResults))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Records) == 0 {
				return res, nil
			}
			r := p.Records[0]
			codes := "," + strings.ReplaceAll(r.Results, " ", "") + ","
			status := "invalid" // no PS01 = did not verify
			if strings.Contains(codes, ",PS01,") {
				if strings.Contains(codes, ",PS08,") {
					status = "valid_landline"
				} else {
					status = "valid_unknown" // line type not asserted in default Express mode
				}
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.90}
			if r.InternationalPhoneNumber != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: r.InternationalPhoneNumber, Confidence: 0.65}
			}
			return res, nil
		},
	}
}
