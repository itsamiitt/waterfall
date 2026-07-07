package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Kaspr builds an adapter for the Kaspr LinkedIn-profile enrichment API (docs/03 §7).
//   - Endpoint: POST {base} with JSON {id, name}  (base default
//     https://api.developers.kaspr.io/profile/linkedin) [docs.developers.kaspr.io].
//   - Auth: API key as the RAW value of the "Authorization" header (no "Bearer" prefix); a second
//     header "accept-version: v2.0" selects API v2 (required). Key injected at egress.
//   - Input: linkedin_url (id) + full_name (name). Fills work_email, personal_email (directEmail),
//     mobile_phone, first/last name, linkedin_url.
//   - Status: DEPRIORITIZED (ADR-0009) — LinkedIn/community contact provenance. 402 = out of credits.
//
// VERIFIED from Kaspr help center + Cargo integration: endpoint, raw-Authorization + accept-version
// auth, request body, flat response field names (workEmail/directEmail/phone/…). Response nesting
// pinned UNVERIFIED (JS-SPA docs) — confirm against a live call (see hunter.go).
func Kaspr(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.developers.kaspr.io/profile/linkedin"
	}
	return &provider.HTTPAdapter{
		NameV:   "kaspr",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization", // raw key, no "Bearer " prefix
			KeyPoolSelector: "kaspr:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldPersonalEmail, Cost: 8, ExpectedConfidence: 0.70},
			{Field: domain.FieldMobilePhone, Cost: 8, ExpectedConfidence: 0.75},
			{Field: domain.FieldFirstName, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldLastName, Cost: 8, ExpectedConfidence: 0.80},
			{Field: domain.FieldLinkedInURL, Cost: 8, ExpectedConfidence: 0.85},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload := map[string]any{"dataToGet": []string{"Phone", "Work email", "Direct email"}}
			putIf2(payload, "id", req.Known[domain.FieldLinkedInURL])
			putIf2(payload, "name", req.Known[domain.FieldFullName])
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("accept-version", "v2.0")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				FirstName   string `json:"firstName"`
				LastName    string `json:"lastName"`
				LinkedIn    string `json:"linkedin"`
				WorkEmail   string `json:"workEmail"`
				DirectEmail string `json:"directEmail"`
				Phone       string `json:"phone"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldWorkEmail, p.WorkEmail, 0.75)
			put(domain.FieldPersonalEmail, p.DirectEmail, 0.70)
			put(domain.FieldMobilePhone, p.Phone, 0.75)
			put(domain.FieldFirstName, p.FirstName, 0.80)
			put(domain.FieldLastName, p.LastName, 0.80)
			put(domain.FieldLinkedInURL, p.LinkedIn, 0.85)
			return res, nil
		},
	}
}

// putIf2 adds k=v to a map[string]any only when v is non-empty.
func putIf2(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}
