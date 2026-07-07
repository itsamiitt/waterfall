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

// SixSense builds an adapter for the 6sense Lead Scoring & Firmographics API (docs/03 §7) — the one
// L8-intent provider that keys on a canonical identity (email/domain/company) rather than a visitor
// IP or an account feed.
//   - Endpoint: POST {base} with an application/x-www-form-urlencoded body (base default
//     https://scribe.6sense.com/v2/people/full) [support.6sense.com/docs].
//   - Auth: token in the "Authorization" header with a LITERAL "Token " prefix — the key-pool secret
//     must be stored as "Token <token>", injected at egress (AuthAPIKeyHeader on "Authorization").
//   - Input: work_email (required by 6sense) + company_hq_country (6sense requires `country`),
//     company_domain, company_name, job_title, first_name, last_name. Fills predictive intent
//     (intent_score, buying_signal=buying stage, intent_topics=segment names) + firmographics.
//
// VERIFIED from docs: endpoint/host, "Token" auth, form-encoded body, request params, scores[] +
// segments + firmographic field names. Body encoding is form-urlencoded (NOT JSON). Field names
// pinned UNVERIFIED until a live authorized call (see hunter.go).
func SixSense(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://scribe.6sense.com/v2/people/full"
	}
	return &provider.HTTPAdapter{
		NameV:   "6sense",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Authorization",
			KeyPoolSelector: "6sense:default", // secret stored as "Token <token>"
		},
		Caps: []provider.Capability{
			{Field: domain.FieldIntentScore, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldBuyingSignal, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldIntentTopics, Cost: 5, ExpectedConfidence: 0.60},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.90},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.75},
			{Field: domain.FieldCompanyPhone, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldNAICS, Cost: 5, ExpectedConfidence: 0.80},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.80},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			form := url.Values{}
			set := func(k, v string) {
				if v != "" {
					form.Set(k, v)
				}
			}
			set("email", req.Known[domain.FieldWorkEmail])
			set("country", req.Known[domain.FieldCompanyHQCountry])
			set("website", req.Known[domain.FieldCompanyDomain])
			set("company", req.Known[domain.FieldCompanyName])
			set("title", req.Known[domain.FieldJobTitle])
			set("firstname", req.Known[domain.FieldFirstName])
			set("lastname", req.Known[domain.FieldLastName])
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Domain        string `json:"domain"`
				Name          string `json:"name"`
				Industry      string `json:"industry"`
				EmployeeCount int64  `json:"employeeCount"`
				AnnualRevenue int64  `json:"annualRevenue"`
				Country       string `json:"country"`
				City          string `json:"city"`
				CompanyPhone  string `json:"companyPhone"`
				SICCode       string `json:"siccode"`
				NAICSCode     string `json:"naicscode"`
				Scores        []struct {
					CompanyIntentScore int64  `json:"company_intent_score"`
					CompanyBuyingStage string `json:"company_buying_stage"`
				} `json:"scores"`
				Segments struct {
					Names []string `json:"names"`
				} `json:"segments"`
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
			put(domain.FieldCompanyDomain, p.Domain, 0.90)
			put(domain.FieldCompanyName, p.Name, 0.85)
			put(domain.FieldIndustry, p.Industry, 0.80)
			put(domain.FieldCompanyHQCountry, p.Country, 0.80)
			put(domain.FieldCompanyHQCity, p.City, 0.75)
			put(domain.FieldCompanyPhone, p.CompanyPhone, 0.65)
			put(domain.FieldNAICS, p.NAICSCode, 0.80)
			put(domain.FieldSIC, p.SICCode, 0.80)
			put(domain.FieldIntentTopics, normList(p.Segments.Names), 0.60)
			if p.EmployeeCount > 0 {
				put(domain.FieldEmployeeCount, itoa(p.EmployeeCount), 0.80)
			}
			if p.AnnualRevenue > 0 {
				put(domain.FieldCompanyRevenue, itoa(p.AnnualRevenue), 0.75)
			}
			if len(p.Scores) > 0 {
				put(domain.FieldBuyingSignal, p.Scores[0].CompanyBuyingStage, 0.85)
				if p.Scores[0].CompanyIntentScore > 0 {
					put(domain.FieldIntentScore, itoa(p.Scores[0].CompanyIntentScore), 0.85)
				}
			}
			return res, nil
		},
	}
}
