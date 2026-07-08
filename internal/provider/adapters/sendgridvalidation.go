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

// SendGridValidation builds an adapter for the Twilio SendGrid Email Address Validation API
// (docs/03 §9).
//   - Endpoint: POST {base}/v3/validations/email with JSON {email}  (base default
//     https://api.sendgrid.com) [twilio.com/docs/sendgrid/api-reference/email-address-validation].
//   - Auth: bearer token — MUST be a dedicated Email Validation API key (separate from a normal Full
//     Access key; Email API Pro/Premier tier only), injected at egress (AuthBearer).
//   - Input: work_email. Fills: email_status (result.verdict: Valid|Risky|Invalid).
//   - Quirk: 403 = key lacks Email-Address-Validation permission or account below Pro/Premier tier;
//     7 req/s ceiling. Real-time single-email endpoint only (bulk is a separate CSV flow).
//
// VERIFIED from docs: endpoint, bearer auth, result.verdict enum, tier/permission requirement. Exact
// field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func SendGridValidation(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.sendgrid.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "sendgrid-validation",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBearer,
			KeyPoolSelector: "sendgrid-validation:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload, err := json.Marshal(map[string]string{"email": req.Known[domain.FieldWorkEmail]})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/v3/validations/email", bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Result struct {
					Verdict string `json:"verdict"`
				} `json:"result"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Result.Verdict != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Result.Verdict, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
