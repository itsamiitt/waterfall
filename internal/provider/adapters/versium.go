package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Versium builds an adapter for the Versium REACH Contact Append API (docs/03 §7).
//   - Endpoint: GET {base}?first=&last=&email=&phone=&output[]=email&output[]=phone&output[]=phone_mobile
//     (base default https://api.versium.com/v2/contact) [api-documentation.versium.com].
//   - Auth: API key in the "x-versium-api-key" header (case-sensitive value), injected at egress.
//   - Input: first_name / last_name / personal_email / mobile_phone. Fills consumer contact-append
//     fields from versium.results[0]. No match = HTTP 200 with num_matches:0 / empty results[].
//   - Status: DEPRIORITIZED (ADR-0009) — B2B2C consumer identity-graph provenance. US-only. 402 =
//     out of match credits (QUOTA); 403 = permission (AUTH).
//
// VERIFIED from docs: endpoint, x-versium-api-key auth, input params, output[] config, result field
// names ("First Name"/"Last Name"/"Email Address"/"Mobile Phone"). Field names pinned UNVERIFIED
// until a live authorized call (see hunter.go). output[] is config (not a canonical input) — set here.
func Versium(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.versium.com/v2/contact"
	}
	return &provider.HTTPAdapter{
		NameV:   "versium",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "x-versium-api-key",
			KeyPoolSelector: "versium:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldFirstName, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldLastName, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldPersonalEmail, Cost: 6, ExpectedConfidence: 0.70},
			{Field: domain.FieldMobilePhone, Cost: 6, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "first", req.Known[domain.FieldFirstName])
			setIf(q, "last", req.Known[domain.FieldLastName])
			setIf(q, "email", req.Known[domain.FieldPersonalEmail])
			setIf(q, "phone", req.Known[domain.FieldMobilePhone])
			// output[] selects which append data to return; config, not a canonical input.
			q.Add("output[]", "email")
			q.Add("output[]", "phone")
			q.Add("output[]", "phone_mobile")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Versium struct {
					Results []struct {
						First  string `json:"First Name"`
						Last   string `json:"Last Name"`
						Email  string `json:"Email Address"`
						Mobile string `json:"Mobile Phone"`
					} `json:"results"`
				} `json:"versium"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Versium.Results) == 0 {
				return res, nil
			}
			r := p.Versium.Results[0]
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldFirstName, r.First, 0.70)
			put(domain.FieldLastName, r.Last, 0.70)
			put(domain.FieldPersonalEmail, r.Email, 0.70)
			put(domain.FieldMobilePhone, r.Mobile, 0.70)
			return res, nil
		},
	}
}
