package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Cloudmersive builds an adapter for the Cloudmersive full email validation endpoint (docs/03 §9).
//   - Endpoint: POST {base}/validate/email/address/full  (base default https://api.cloudmersive.com);
//     the request body is a BARE JSON STRING — the email enclosed in double quotes, not an object
//     [github.com/Cloudmersive/Cloudmersive.APIClient.NodeJS.Validate EmailApi.md].
//   - Auth: API key in the "Apikey" HEADER, injected at egress (AuthAPIKeyHeader).
//   - Input: work_email. Fills: email_status (boolean ValidAddress — syntax+MX+live SMTP combined —
//     rendered valid|invalid) + company_domain (the parsed Domain; may be a free-mail domain).
//   - Quirk: an undeliverable address is HTTP 200 with ValidAddress=false (normal negative verdict);
//     all response fields are optional/nullable, keys are PascalCase/underscore (ValidAddress,
//     Valid_SMTP, Domain).
//
// VERIFIED from the vendor's swagger-generated client source: endpoint, Apikey header, on-the-wire
// key casing. Exact field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Cloudmersive(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.cloudmersive.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "cloudmersive",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Apikey",
			KeyPoolSelector: "cloudmersive:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			// The body is the raw email as a JSON string literal.
			payload, err := json.Marshal(req.Known[domain.FieldWorkEmail])
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/validate/email/address/full", bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				ValidAddress *bool  `json:"ValidAddress"`
				Domain       string `json:"Domain"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.ValidAddress != nil {
				verdict := "invalid"
				if *p.ValidAddress {
					verdict = "valid"
				}
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: verdict, Confidence: 0.90}
			}
			if p.Domain != "" {
				res.Values[domain.FieldCompanyDomain] = provider.Observation{Value: p.Domain, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
