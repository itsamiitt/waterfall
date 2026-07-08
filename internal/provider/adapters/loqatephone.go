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

// LoqatePhone builds an adapter for the Loqate (GBG) Phone Validation API (docs/03 §8).
//   - Endpoint: GET {base}/PhoneNumberValidation/Interactive/Validate/v2.20/json6.ws?Phone=
//     (base default https://api.addressy.com) [docs.loqate.com, error envelope live-verified].
//   - Auth: API key in the "Key" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: mobile_phone (E.164 with + prefix — the optional Country hint has no canonical source).
//     Fills: phone_status (IsValid STRING "Yes"|"No"|"Maybe" + NumberType MOBILE/LANDLINE/VOIP →
//     normalized; "Maybe" = full validation not possible → phone_status omitted as inconclusive)
//   - mobile_phone (the normalized international PhoneNumber, only when IsValid=Yes).
//   - Quirk: ALL Loqate errors arrive as a 4-field object inside Items[] ({Error,Description,Cause,
//     Resolution}); on json6.ws a bad key is HTTP 401 but legacy paths return the SAME body as HTTP
//     200 (both live-verified) — Decode always checks Items[0].Error first and classifies by code:
//     2/4 auth, 3/8/24 quota, 10 rate-limit (surge protector), else bad-request.
//
// VERIFIED from docs + live probes: endpoint, Key query auth, Items envelope, IsValid values, error
// codes. Success field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func LoqatePhone(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.addressy.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "loqate-phone",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "Key",
			KeyPoolSelector: "loqate-phone:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/PhoneNumberValidation/Interactive/Validate/v2.20/json6.ws")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("Phone", req.Known[domain.FieldMobilePhone])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Items []struct {
					Error       string `json:"Error"`
					Description string `json:"Description"`
					PhoneNumber string `json:"PhoneNumber"`
					IsValid     string `json:"IsValid"`
					NumberType  string `json:"NumberType"`
				} `json:"Items"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Items) == 0 {
				return res, nil
			}
			it := p.Items[0]
			// Items-wrapped error envelope (may arrive under HTTP 200 on legacy paths) — check first.
			if it.Error != "" {
				var class domain.ErrorClass
				switch it.Error {
				case "2", "4":
					class = domain.ClassAuth
				case "3", "8", "24":
					class = domain.ClassQuota
				case "10":
					class = domain.ClassRateLimit
				default:
					class = domain.ClassBadRequest
				}
				msg := it.Description
				if msg == "" {
					msg = "loqate error " + it.Error
				}
				return provider.Result{}, domain.NewProviderError("loqate-phone", class, errors.New(msg))
			}
			switch it.IsValid {
			case "Yes":
				res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: phoneStatusFromType(it.NumberType), Confidence: 0.90}
				if it.PhoneNumber != "" {
					res.Values[domain.FieldMobilePhone] = provider.Observation{Value: it.PhoneNumber, Confidence: 0.90}
				}
			case "No":
				res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: "invalid", Confidence: 0.90}
			}
			// "Maybe" (full validation not possible) → inconclusive: omit phone_status entirely.
			return res, nil
		},
	}
}
