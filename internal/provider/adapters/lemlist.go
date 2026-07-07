package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Lemlist builds an async adapter for the Lemlist enrich (email finder + verifier) API (docs/03 §2).
//   - Submit: POST {base}/enrich?findEmail=true&verifyEmail=true&<person query params> → {id:"enr_…"}.
//   - Poll: GET {base}/enrich/{id} until enrichmentStatus == "done" (HTTP 202 "In progress" while running).
//   - Auth: HTTP Basic with an EMPTY username and the API key as the password (docs: "BASIC not
//     bearer", "colon before your API key"), injected at egress (AuthBasic). The "<slug>:default" key
//     pool must hold the full Basic credential ":<API_KEY>" (leading colon; egress base64s it verbatim).
//   - base default https://api.lemlist.com/api [developer.lemlist.com].
//   - Fills work_email + email_status from the GET poll body data.email.{email,notFound}. The richer
//     LinkedIn-enrichment fields are only in the webhook payload (not the GET poll body), so they are
//     intentionally NOT mapped here — mapping them would be fabrication for the poll path.
//   - Quirk: no-match is HTTP 200 with data.email.notFound=true (not an error); 403 = user blocked (AUTH).
//
// VERIFIED from docs: submit/poll endpoints, Basic auth (empty user), id token, enrichmentStatus
// "done", data.email.{email,notFound}. Field names pinned UNVERIFIED until a live key (see hunter.go).
func Lemlist(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.lemlist.com/api"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "lemlist",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBasic, KeyPoolSelector: "lemlist:default"},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 2 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 5, ExpectedConfidence: 0.90},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/enrich")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("findEmail", "true")
			q.Set("verifyEmail", "true")
			setIfQ(q, "linkedinUrl", req.Known[domain.FieldLinkedInURL])
			setIfQ(q, "firstName", req.Known[domain.FieldFirstName])
			setIfQ(q, "lastName", req.Known[domain.FieldLastName])
			setIfQ(q, "companyDomain", req.Known[domain.FieldCompanyDomain])
			setIfQ(q, "companyName", req.Known[domain.FieldCompanyName])
			setIfQ(q, "jobTitle", req.Known[domain.FieldJobTitle])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.ID == "" {
				return "", domain.NewProviderError("lemlist", domain.ClassTransient, errNoJobID)
			}
			return p.ID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/enrich/"+url.PathEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				EnrichmentStatus string `json:"enrichmentStatus"`
				Data             struct {
					Email struct {
						Email    string `json:"email"`
						NotFound bool   `json:"notFound"`
					} `json:"email"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			if p.EnrichmentStatus != "done" {
				return provider.Result{}, false, nil // still in progress
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if !p.Data.Email.NotFound && p.Data.Email.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Data.Email.Email, Confidence: 0.85}
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: "deliverable", Confidence: 0.90}
			}
			return res, true, nil
		},
	}
}
