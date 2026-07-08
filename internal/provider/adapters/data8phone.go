package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Data8Phone builds an adapter for the Data8 PhoneValidation IsValid API (docs/03 §8).
//   - Endpoint: POST {base} with JSON {telephoneNumber, defaultCountry:"auto"}  (base default
//     https://webservices.data-8.co.uk/PhoneValidation/IsValid.json) [docs.data-8.co.uk].
//   - Auth: API key in the "key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: mobile_phone. Fills: phone_status (Result.ValidationResult Valid|Invalid|NoCoverage|
//     Unavailable|Ambiguous combined with Result.NumberType Mobile|Landline|… → normalized;
//     NoCoverage/Unavailable/Ambiguous are inconclusive → omitted) + mobile_phone (the normalized
//     Result.TelephoneNumber echo, only when Valid, ~0.65).
//   - Quirk: errors are HTTP 200 with Status.Success=false + Status.ErrorMessage (auth/out-of-credits)
//     — Decode classifies the message. NoCoverage results are not charged.
//
// VERIFIED from docs: endpoint, key query auth, Status/Result shape, ValidationResult+NumberType
// enums. Exact field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Data8Phone(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://webservices.data-8.co.uk/PhoneValidation/IsValid.json"
	}
	return &provider.HTTPAdapter{
		NameV:   "data8-phone",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "key",
			KeyPoolSelector: "data8-phone:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 1, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload, err := json.Marshal(map[string]string{
				"telephoneNumber": req.Known[domain.FieldMobilePhone],
				"defaultCountry":  "auto",
			})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Status struct {
					Success      bool   `json:"Success"`
					ErrorMessage string `json:"ErrorMessage"`
				} `json:"Status"`
				Result struct {
					TelephoneNumber  string `json:"TelephoneNumber"`
					ValidationResult string `json:"ValidationResult"`
					NumberType       string `json:"NumberType"`
				} `json:"Result"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body: Success=false (auth / out of credits).
			if !p.Status.Success {
				return provider.Result{}, bodyErr("data8-phone", p.Status.ErrorMessage)
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			switch p.Result.ValidationResult {
			case "Valid":
				res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: phoneStatusFromType(p.Result.NumberType), Confidence: 0.90}
				if p.Result.TelephoneNumber != "" {
					res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.Result.TelephoneNumber, Confidence: 0.65}
				}
			case "Invalid":
				res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: "invalid", Confidence: 0.90}
			}
			// NoCoverage / Unavailable / Ambiguous → inconclusive: no phone_status emitted.
			return res, nil
		},
	}
}
