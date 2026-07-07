package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Icypeas builds an async adapter for the Icypeas email-finder API (docs/03 §7).
//   - Submit: POST {base}/email-search with {firstname,lastname,domainOrCompany} → item._id.
//   - Poll: POST {base}/bulk-single-searchs/read with {"id":<token>,"mode":"single"} (the token is
//     carried in the BODY, not the path) until items[0].status is terminal. Pending statuses:
//     NONE/SCHEDULED/IN_PROGRESS. INSUFFICIENT_FUNDS→QUOTA, BAD_INPUT→BAD_REQUEST; DEBITED/FOUND/
//     NOT_FOUND/ABORTED are terminal (email present only when found).
//   - Auth: raw API key in the "Authorization" header (no scheme prefix), injected at egress. The
//     HMAC-SHA1 secret Icypeas documents is for INBOUND webhook verification only — not needed here.
//   - base default https://app.icypeas.com/api [api-doc.icypeas.com].
//
// VERIFIED from docs: endpoints, auth, submit/poll bodies, status enum, results.emails[].{email,
// certainty}. Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Icypeas(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://app.icypeas.com/api"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "icypeas",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "Authorization", KeyPoolSelector: "icypeas:default"},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 5 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 2, ExpectedConfidence: 0.90},
			{Field: domain.FieldEmailStatus, Cost: 2, ExpectedConfidence: 0.85},
			{Field: domain.FieldFirstName, Cost: 2, ExpectedConfidence: 0.95},
			{Field: domain.FieldLastName, Cost: 2, ExpectedConfidence: 0.95},
			{Field: domain.FieldFullName, Cost: 2, ExpectedConfidence: 0.90},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]string{}
			putIf(body, "firstname", req.Known[domain.FieldFirstName])
			putIf(body, "lastname", req.Known[domain.FieldLastName])
			putIf(body, "domainOrCompany", req.Known[domain.FieldCompanyDomain])
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/email-search", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Item struct {
					ID string `json:"_id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.Item.ID == "" {
				return "", domain.NewProviderError("icypeas", domain.ClassTransient, errNoJobID)
			}
			return p.Item.ID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			b, err := json.Marshal(map[string]string{"id": token, "mode": "single"})
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bulk-single-searchs/read", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Items []struct {
					Status  string `json:"status"`
					Results struct {
						FirstName string `json:"firstname"`
						LastName  string `json:"lastname"`
						FullName  string `json:"fullname"`
						Emails    []struct {
							Email     string `json:"email"`
							Certainty string `json:"certainty"`
						} `json:"emails"`
					} `json:"results"`
				} `json:"items"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if len(p.Items) == 0 {
				return provider.Result{}, false, nil // not ready
			}
			it := p.Items[0]
			switch it.Status {
			case "NONE", "SCHEDULED", "IN_PROGRESS", "":
				return provider.Result{}, false, nil
			case "INSUFFICIENT_FUNDS":
				return provider.Result{}, false, domain.NewProviderError("icypeas", domain.ClassQuota, errResultsGone)
			case "BAD_INPUT":
				return provider.Result{}, false, domain.NewProviderError("icypeas", domain.ClassBadRequest, errResultsGone)
			}
			// terminal (DEBITED/FOUND/NOT_FOUND/DEBITED_NOT_FOUND/ABORTED): map what's present.
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldFirstName, it.Results.FirstName, 0.95)
			put(domain.FieldLastName, it.Results.LastName, 0.95)
			put(domain.FieldFullName, it.Results.FullName, 0.90)
			if len(it.Results.Emails) > 0 {
				put(domain.FieldWorkEmail, it.Results.Emails[0].Email, 0.90)
				put(domain.FieldEmailStatus, it.Results.Emails[0].Certainty, 0.85)
			}
			return res, true, nil
		},
	}
}
