package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// SendPulseVerifier builds an async adapter for the SendPulse Email Verifier (docs/03 §9).
//   - Auth: oauth2-cc — POST {base}/oauth/access_token with JSON {grant_type,client_id,client_secret}
//     (TokenStyle "json"; pool secret "<client_id>:<client_secret>") → 1h Bearer.
//   - Submit: POST {base}/verifier-service/send-single-to-verify/ {email} → {"result":true} — the
//     submit body carries NO job id, so the poll token is the SUBMITTED EMAIL itself
//     (TokenFromRequest, ADR-0024 extension); ParseSubmit still validates the result flag.
//   - Poll: GET {base}/verifier-service/get-single-result/?email= — {"result":false} = still pending
//     (the docs' "false error" also covers never-submitted; the bounded budget caps the loop);
//     {"result":true,"data":{…}} = terminal.
//   - base default https://api.sendpulse.com [sendpulse.com/integrations/api/verifier].
//   - Fills email_status (data.checks.status_text, e.g. "Valid address"; numeric twin 0-3) +
//     work_email (echoed data.email).
//
// VERIFIED from docs: token exchange, paired endpoints, result-flag semantics, checks fields. Poll
// interval undocumented (2s engineering choice). Field names pinned UNVERIFIED until a live key.
func SendPulseVerifier(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.sendpulse.com"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:   "sendpulse-verifier",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthOAuth2CC,
			KeyPoolSelector: "sendpulse-verifier:default",
			TokenURL:        base + "/oauth/access_token", // same host — one SSRF allow-list entry
			TokenStyle:      "json",
		},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 2 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.90},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload, err := json.Marshal(map[string]string{"email": req.Known[domain.FieldWorkEmail]})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/verifier-service/send-single-to-verify/", bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		// Validates the submit body only; the poll token is the email (TokenFromRequest below).
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Result bool `json:"result"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if !p.Result {
				return "", domain.NewProviderError("sendpulse-verifier", domain.ClassTransient,
					errors.New("submit not accepted (result=false)"))
			}
			return "", nil
		},
		TokenFromRequest: func(req provider.Request) string {
			return req.Known[domain.FieldWorkEmail]
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet,
				strings.TrimRight(base, "/")+"/verifier-service/get-single-result/?email="+url.QueryEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Result bool `json:"result"`
				Data   struct {
					Email  string `json:"email"`
					Checks struct {
						StatusText string `json:"status_text"`
					} `json:"checks"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if !p.Result {
				return provider.Result{}, false, nil // still pending (docs' "false error")
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Data.Checks.StatusText != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Data.Checks.StatusText, Confidence: 0.90}
			}
			if p.Data.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Data.Email, Confidence: 0.90}
			}
			return res, true, nil
		},
	}
}
