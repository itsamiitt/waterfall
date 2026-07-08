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

// MailsSo builds an adapter for the Mails.so single email validation API (docs/03 §9).
//   - Endpoint: GET {base}/v1/validate?email=  (base default https://api.mails.so)
//     [docs.mails.so/single].
//   - Auth: API key in the "x-mails-api-key" HEADER, injected at egress (AuthAPIKeyHeader).
//   - Input: work_email. Fills: email_status (data.result: deliverable|undeliverable|risky|unknown)
//   - work_email (the echoed data.email).
//   - Quirk: every response is a {data,error} envelope — a non-null error string on 200 is an
//     in-body failure (Decode classifies it); result:"unknown"/reason:"timeout" is a soft,
//     inconclusive verdict, not an error. A plan gate ("Paid subscription required…") surfaces as
//     HTTP 401 on the batch endpoint.
//
// VERIFIED from docs (raw MDX source): endpoint, x-mails-api-key header, {data,error} envelope,
// result/reason enums. Exact field names pinned UNVERIFIED until a live key (see hunter.go).
func MailsSo(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.mails.so"
	}
	return &provider.HTTPAdapter{
		NameV:   "mails-so",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "x-mails-api-key",
			KeyPoolSelector: "mails-so:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.90},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/v1/validate")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("email", req.Known[domain.FieldWorkEmail])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Data struct {
					Email  string `json:"email"`
					Result string `json:"result"`
				} `json:"data"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// Envelope error (e.g. "Unauthorized", plan gate) — classify the message.
			if p.Error != "" {
				return provider.Result{}, bodyErr("mails-so", p.Error)
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Data.Result != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Data.Result, Confidence: 0.90}
			}
			if p.Data.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Data.Email, Confidence: 0.90}
			}
			return res, nil
		},
	}
}
