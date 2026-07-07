package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Snov builds an async adapter for the Snov.io v2 Email Finder API (docs/03 §7).
//   - Auth: OAuth2 client-credentials, TokenStyle "body" — the egress AuthInjector POSTs
//     grant_type/client_id/client_secret (form-encoded, pool secret "clientId:clientSecret") to
//     {token_url}, caches the Bearer for its hour lifetime, and injects it on both hops.
//   - Submit: POST {base}/v2/emails-by-domain-by-name/start with {"rows":[{first_name,last_name,
//     domain}]} → data.task_hash.
//   - Poll: GET {base}/v2/emails-by-domain-by-name/result?task_hash={hash} until status=="completed".
//   - base default https://api.snov.io [snov.io/api].
//
// VERIFIED from docs: oauth2 client-credentials (body form), v2 start/result endpoints, task_hash,
// status completed/in_progress, result[].{email,smtp_status}. Terminal field names pinned UNVERIFIED
// until a live authorized call (see hunter.go). First consumer of oauth2-cc TokenStyle "body".
func Snov(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.snov.io"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:   "snov",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthOAuth2CC,
			KeyPoolSelector: "snov:default",
			TokenURL:        base + "/v1/oauth/access_token",
			TokenStyle:      "body",
		},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 3 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 2, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.80},
			{Field: domain.FieldFullName, Cost: 2, ExpectedConfidence: 0.55},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			row := map[string]string{}
			putIf(row, "first_name", req.Known[domain.FieldFirstName])
			putIf(row, "last_name", req.Known[domain.FieldLastName])
			putIf(row, "domain", req.Known[domain.FieldCompanyDomain])
			b, err := json.Marshal(map[string]any{"rows": []map[string]string{row}})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v2/emails-by-domain-by-name/start", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Data struct {
					TaskHash string `json:"task_hash"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.Data.TaskHash == "" {
				return "", domain.NewProviderError("snov", domain.ClassTransient, errNoJobID)
			}
			return p.Data.TaskHash, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/emails-by-domain-by-name/result?task_hash="+url.QueryEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Status string `json:"status"`
				Data   []struct {
					People string `json:"people"`
					Result []struct {
						Email      string `json:"email"`
						SMTPStatus string `json:"smtp_status"`
					} `json:"result"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if p.Status != "completed" {
				return provider.Result{}, false, nil // in_progress
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Data) == 0 {
				return res, true, nil
			}
			d := p.Data[0]
			if d.People != "" {
				res.Values[domain.FieldFullName] = provider.Observation{Value: d.People, Confidence: 0.55}
			}
			if len(d.Result) > 0 {
				if d.Result[0].Email != "" {
					res.Values[domain.FieldWorkEmail] = provider.Observation{Value: d.Result[0].Email, Confidence: 0.85}
				}
				if d.Result[0].SMTPStatus != "" {
					res.Values[domain.FieldEmailStatus] = provider.Observation{Value: d.Result[0].SMTPStatus, Confidence: 0.80}
				}
			}
			return res, true, nil
		},
	}
}
