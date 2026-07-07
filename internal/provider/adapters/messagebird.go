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

// MessageBird builds an adapter for the MessageBird (Bird) Lookup API (docs/03 §7).
//   - Endpoint: GET {base}/{e164}  (base default https://rest.messagebird.com/lookup)
//     [developers.messagebird.com/api/lookup].
//   - Auth: proprietary header "Authorization: AccessKey <key>" — the key-pool secret must be
//     stored WITH the "AccessKey " prefix, injected at egress (AuthAPIKeyHeader on "Authorization").
//   - Input: mobile_phone. Fills: phone_status from `type` (mobile/fixed line/voip/…) + echoed
//     E.164 mobile_phone from formats.e164. Unparseable numbers return HTTP 422 (BAD_REQUEST).
//
// VERIFIED from docs: endpoint, AccessKey header, `type` + `formats.e164`. Field names pinned
// UNVERIFIED until a live authorized call (see hunter.go).
func MessageBird(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://rest.messagebird.com/lookup"
	}
	return &provider.HTTPAdapter{
		NameV:   "messagebird",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization",
			KeyPoolSelector: "messagebird:default", // secret stored as "AccessKey <key>"
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
				Type    string `json:"type"`
				Formats struct {
					E164 string `json:"e164"`
				} `json:"formats"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			res.Values[domain.FieldPhoneStatus] = provider.Observation{Value: phoneStatusFromType(p.Type), Confidence: 0.90}
			if p.Formats.E164 != "" {
				res.Values[domain.FieldMobilePhone] = provider.Observation{Value: p.Formats.E164, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
