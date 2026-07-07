package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// AnymailFinder builds an adapter for the Anymail Finder find-person-email API (docs/03 §7).
//   - Endpoint: POST {base} with JSON match keys (base default
//     https://api.anymailfinder.com/v5.1/find-email/person) [anymailfinder.com/email-finder-api/docs].
//   - Auth: API key as the RAW value of the "Authorization" header (NO "Bearer" prefix) — modeled as
//     AuthAPIKeyHeader with HeaderName "Authorization", injected at egress.
//   - Input: full_name / first+last, company_domain, company_name, linkedin_url. Fills: work_email
//     (only charged when valid), email_status (valid|risky|not_found|blacklisted), and, only for
//     LinkedIn-sourced results, person full_name / job_title (low coverage).
//   - No 200-with-error quirk: bad key -> 401 (AUTH), out-of-credits -> 402 (QUOTA). No match = 200
//     with email_status="not_found".
//
// VERIFIED from docs: endpoint, raw-Authorization-header auth, request params, success-body fields,
// status vocabulary, error codes. Field names pinned UNVERIFIED until a live authorized call.
func AnymailFinder(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.anymailfinder.com/v5.1/find-email/person"
	}
	return &provider.HTTPAdapter{
		NameV:   "anymailfinder",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization", // raw key, no "Bearer " prefix (official docs)
			KeyPoolSelector: "anymailfinder:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 9, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmailStatus, Cost: 9, ExpectedConfidence: 0.90},
			{Field: domain.FieldFullName, Cost: 9, ExpectedConfidence: 0.40},
			{Field: domain.FieldJobTitle, Cost: 9, ExpectedConfidence: 0.40},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload := map[string]string{}
			putIf(payload, "full_name", req.Known[domain.FieldFullName])
			putIf(payload, "first_name", req.Known[domain.FieldFirstName])
			putIf(payload, "last_name", req.Known[domain.FieldLastName])
			putIf(payload, "domain", req.Known[domain.FieldCompanyDomain])
			putIf(payload, "company_name", req.Known[domain.FieldCompanyName])
			putIf(payload, "linkedin_url", req.Known[domain.FieldLinkedInURL])
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Email       string `json:"email"`
				EmailStatus string `json:"email_status"`
				FullName    string `json:"person_full_name"`
				JobTitle    string `json:"person_job_title"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Email != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Email, Confidence: 0.85}
			}
			if p.EmailStatus != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.EmailStatus, Confidence: 0.90}
			}
			if p.FullName != "" {
				res.Values[domain.FieldFullName] = provider.Observation{Value: p.FullName, Confidence: 0.40}
			}
			if p.JobTitle != "" {
				res.Values[domain.FieldJobTitle] = provider.Observation{Value: p.JobTitle, Confidence: 0.40}
			}
			return res, nil
		},
	}
}
