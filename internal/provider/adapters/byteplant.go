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

// Byteplant builds an adapter for the Byteplant phone-validator.net v2 API (docs/03 §7).
//   - Endpoint: GET {base}?PhoneNumber=  (base default https://api.phone-validator.net/api/v2/verify)
//     [byteplant.com/phone-validator/api.html].
//   - Auth: API key in the "APIKey" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: mobile_phone. Fills: phone_status (from `status` + `linetype`) + echoed E.164
//     (`formatinternational`).
//   - Quirk: outcomes are carried in the `status` string (HTTP 200): API_KEY_INVALID_OR_DEPLETED →
//     AUTH, RATE_LIMIT_EXCEEDED → RATE_LIMIT, DELAYED → TRANSIENT (all classified); INVALID →
//     "invalid"; VALID_CONFIRMED/VALID_UNCONFIRMED → line-type-derived status.
//
// VERIFIED from docs: endpoint, APIKey auth, status + linetype vocabularies. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func Byteplant(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.phone-validator.net/api/v2/verify"
	}
	return &provider.HTTPAdapter{
		NameV:   "byteplant-phone",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "APIKey",
			KeyPoolSelector: "byteplant-phone:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.85},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("PhoneNumber", req.Known[domain.FieldMobilePhone])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Status              string `json:"status"`
				LineType            string `json:"linetype"`
				FormatInternational string `json:"formatinternational"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			switch p.Status {
			case "API_KEY_INVALID_OR_DEPLETED":
				return provider.Result{}, domain.NewProviderError("byteplant-phone", domain.ClassAuth, errors.New(p.Status))
			case "RATE_LIMIT_EXCEEDED":
				return provider.Result{}, domain.NewProviderError("byteplant-phone", domain.ClassRateLimit, errors.New(p.Status))
			case "DELAYED":
				return provider.Result{}, domain.NewProviderError("byteplant-phone", domain.ClassTransient, errors.New(p.Status))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			status := "invalid"
			if strings.HasPrefix(p.Status, "VALID") {
				status = phoneStatusFromType(p.LineType)
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.85}
			if p.FormatInternational != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.FormatInternational, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
