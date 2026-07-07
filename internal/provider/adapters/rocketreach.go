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

// RocketReach builds an async adapter for the RocketReach v2 Person Lookup API (docs/03 §7).
//   - Submit: GET {base}/person/lookup?name=&current_employer=&title=&email=&linkedin_url= → id.
//   - Poll: GET {base}/person/checkStatus?ids={id} (root-level array) until [0].status in
//     {complete,failed} (waiting/searching/progress pending).
//   - Auth: API key in the "Api-Key" header, injected at egress.
//   - Status: DEPRIORITIZED (ADR-0009) — LinkedIn/public-web provenance.
//   - base default https://api.rocketreach.co/api/v2 [docs.rocketreach.co].
//
// VERIFIED from docs + SDK: endpoints, Api-Key header, id token, status enum, [].emails[]{email,
// type,grade} + phones[]{number,type} + current_title/current_employer/linkedin_url. Field names
// pinned UNVERIFIED until a live authorized call (see hunter.go).
func RocketReach(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.rocketreach.co/api/v2"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "rocketreach",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "Api-Key", KeyPoolSelector: "rocketreach:default"},
		Policy:       provider.CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1},
		PollInterval: 5 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.85},
			{Field: domain.FieldPersonalEmail, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.70},
			{Field: domain.FieldMobilePhone, Cost: 3, ExpectedConfidence: 0.75},
			{Field: domain.FieldLinkedInURL, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldJobTitle, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldFullName, Cost: 3, ExpectedConfidence: 0.90},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/person/lookup")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "name", req.Known[domain.FieldFullName])
			setIf(q, "current_employer", req.Known[domain.FieldCompanyName])
			setIf(q, "title", req.Known[domain.FieldJobTitle])
			setIf(q, "email", req.Known[domain.FieldWorkEmail])
			setIf(q, "linkedin_url", req.Known[domain.FieldLinkedInURL])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.ID == 0 {
				return "", domain.NewProviderError("rocketreach", domain.ClassNotFound, errResultsGone)
			}
			return itoa(p.ID), nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/person/checkStatus?ids="+url.QueryEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var arr []struct {
				Status       string `json:"status"`
				Name         string `json:"name"`
				CurrentTitle string `json:"current_title"`
				CurrentEmp   string `json:"current_employer"`
				LinkedInURL  string `json:"linkedin_url"`
				Emails       []struct {
					Email string `json:"email"`
					Type  string `json:"type"`
					Grade string `json:"grade"`
				} `json:"emails"`
				Phones []struct {
					Number string `json:"number"`
					Type   string `json:"type"`
				} `json:"phones"`
			}
			if err := json.Unmarshal(body, &arr); err != nil {
				return provider.Result{}, false, err
			}
			if len(arr) == 0 {
				return provider.Result{}, false, nil
			}
			p := arr[0]
			switch p.Status {
			case "complete":
				// terminal — map below
			case "waiting", "searching", "progress", "":
				return provider.Result{}, false, nil
			default: // failed
				return provider.Result{}, true, nil
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldFullName, p.Name, 0.90)
			put(domain.FieldJobTitle, p.CurrentTitle, 0.90)
			put(domain.FieldCompanyName, p.CurrentEmp, 0.90)
			put(domain.FieldLinkedInURL, p.LinkedInURL, 0.90)
			for _, e := range p.Emails {
				switch e.Type {
				case "professional":
					put(domain.FieldWorkEmail, e.Email, 0.85)
				case "personal":
					put(domain.FieldPersonalEmail, e.Email, 0.80)
				}
			}
			if len(p.Emails) > 0 {
				put(domain.FieldEmailStatus, p.Emails[0].Grade, 0.70)
			}
			for _, ph := range p.Phones {
				if ph.Type == "mobile" {
					put(domain.FieldMobilePhone, ph.Number, 0.75)
				}
			}
			return res, true, nil
		},
	}
}
