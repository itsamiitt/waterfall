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

// Twilio builds an adapter for Twilio Lookup v2 phone validation (docs/03).
//   - Endpoint: GET {base}/{e164}?Fields=line_type_intelligence (base default
//     https://lookups.twilio.com/v2/PhoneNumbers).
//   - Auth: HTTP Basic (AccountSid:AuthToken), injected at egress (AuthBasic).
//   - Quirk: 404 -> number not found -> ClassNotFound (success-with-no-value).
//   - Input: mobile_phone (the number to validate). Fills: phone_status (+ mobile line type).
//
// Wire format is REPRESENTATIVE / `UNVERIFIED` — confirm against live docs (see hunter.go).
func Twilio(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://lookups.twilio.com/v2/PhoneNumbers"
	}
	return &provider.HTTPAdapter{
		NameV:   "twilio-lookup",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBasic,
			KeyPoolSelector: "twilio:default", // resolves to "AccountSid:AuthToken"
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.95},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			phone := req.Known[domain.FieldMobilePhone]
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(phone)
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			q := r.URL.Query()
			q.Set("Fields", "line_type_intelligence")
			r.URL.RawQuery = q.Encode()
			return r, nil
		},
		Decode: decodeTwilio,
	}
}

func decodeTwilio(body []byte) (provider.Result, error) {
	var p struct {
		Valid                bool `json:"valid"`
		LineTypeIntelligence struct {
			Type string `json:"type"` // "mobile" | "landline" | "voip" | ...
		} `json:"line_type_intelligence"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	status := "invalid"
	conf := domain.Confidence(0.60)
	if p.Valid {
		status = "valid"
		conf = 0.95
	}
	res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: conf}
	return res, nil
}
