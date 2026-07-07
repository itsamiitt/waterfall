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

// IPQualityScore builds an adapter for the IPQualityScore Phone Validation API (docs/03 §7).
//   - Endpoint: GET {base}/{phone}  (base default https://www.ipqualityscore.com/api/json/phone)
//     [ipqualityscore.com/documentation/phone-number-validation-api].
//   - Auth: API key in the "IPQS-KEY" header, injected at egress (AuthAPIKeyHeader) — keeps the key
//     out of the URL (the path-embedded key form the docs default to can't be injected at egress).
//   - Input: mobile_phone. Fills: phone_status (from valid + line_type) + echoed E.164 (`formatted`).
//   - Quirk: processing/auth/quota failures come as HTTP 200 with `success:false` + a `message`;
//     Decode classifies that via classifyErrMsg. valid:false on success:true is a normal "invalid".
//
// VERIFIED from docs: endpoint, header/param key methods, success/valid/line_type/formatted fields.
// Exact path form under header auth pinned UNVERIFIED until a live authorized call (see hunter.go).
func IPQualityScore(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://www.ipqualityscore.com/api/json/phone"
	}
	return &provider.HTTPAdapter{
		NameV:   "ipqualityscore",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "IPQS-KEY",
			KeyPoolSelector: "ipqualityscore:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldPhoneStatus, Cost: 5, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/" + url.PathEscape(req.Known[domain.FieldMobilePhone])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Success   bool   `json:"success"`
				Valid     bool   `json:"valid"`
				LineType  string `json:"line_type"`
				Formatted string `json:"formatted"`
				Message   string `json:"message"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			if !p.Success {
				return provider.Result{}, domain.NewProviderError("ipqualityscore", classifyErrMsg(p.Message), errors.New(p.Message))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			status := "invalid"
			if p.Valid {
				status = phoneStatusFromType(p.LineType)
			}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: status, Confidence: 0.90}
			if p.Formatted != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.Formatted, Confidence: 0.95}
			}
			return res, nil
		},
	}
}
