package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// BuiltWith builds an adapter for the BuiltWith Domain API v23 (docs/03 §7) — website technology
// detection (technographics).
//   - Endpoint: GET {base}?LOOKUP=  (base default https://api.builtwith.com/v23/api.json)
//     [api.builtwith.com/domain-api].
//   - Auth: API key (GUID) in the "KEY" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: company_domain. Fills: technographics (sorted, comma-joined detected tech names) plus
//     firmographic Meta/Attributes (company_name, industry, hq country, employee_count, revenue).
//   - Quirk: BuiltWith returns application errors as a negative Code in an in-body Errors array with
//     HTTP 200 (documented at /errorCodes): -2 bad key -> AUTH, -3 out of credits -> QUOTA,
//     -1/-4/-5/-7/-8 -> BAD_REQUEST, -99 internal -> TRANSIENT. Decode classifies these; a domain
//     not in the DB returns IsDB="False" with empty Technologies (success-with-no-value).
//
// VERIFIED from docs: endpoint, KEY query auth, response shape, error-code list. The exact HTTP
// status carrying in-body error codes is not documented (docs say treat non-200 as errors too), so
// the 200+Code mapping is best-effort; field names pinned UNVERIFIED until a live call.
func BuiltWith(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.builtwith.com/v23/api.json"
	}
	return &provider.HTTPAdapter{
		NameV:   "builtwith",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "KEY",
			KeyPoolSelector: "builtwith:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldTechnographics, Cost: 5, ExpectedConfidence: 0.88},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.55},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.55},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("LOOKUP", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: decodeBuiltWith,
	}
}

func decodeBuiltWith(body []byte) (provider.Result, error) {
	var p struct {
		Results []struct {
			Result struct {
				Paths []struct {
					Technologies []struct {
						Name string `json:"Name"`
					} `json:"Technologies"`
				} `json:"Paths"`
			} `json:"Result"`
			Meta struct {
				CompanyName string `json:"CompanyName"`
				Country     string `json:"Country"`
				Vertical    string `json:"Vertical"`
			} `json:"Meta"`
			Attributes struct {
				Employees int64 `json:"Employees"`
			} `json:"Attributes"`
			SalesRevenue int64 `json:"SalesRevenue"`
		} `json:"Results"`
		Errors []struct {
			Message string `json:"Message"`
			Code    int    `json:"Code"`
		} `json:"Errors"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, err
	}
	// In-body error codes (HTTP 200). Map onto the taxonomy so key failover / alerting works.
	if len(p.Errors) > 0 {
		e := p.Errors[0]
		class := domain.ClassBadRequest
		switch e.Code {
		case -2:
			class = domain.ClassAuth
		case -3:
			class = domain.ClassQuota
		case -99:
			class = domain.ClassTransient
		}
		return provider.Result{}, domain.NewProviderError("builtwith", class, fmt.Errorf("builtwith error %d: %s", e.Code, e.Message))
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if len(p.Results) == 0 {
		return res, nil
	}
	r := p.Results[0]
	var techs []string
	for _, path := range r.Result.Paths {
		for _, t := range path.Technologies {
			techs = append(techs, t.Name)
		}
	}
	if v := normList(techs); v != "" {
		res.Values[domain.FieldTechnographics] = provider.Observation{Value: v, Confidence: 0.88}
	}
	if r.Meta.CompanyName != "" {
		res.Values[domain.FieldCompanyName] = provider.Observation{Value: r.Meta.CompanyName, Confidence: 0.70}
	}
	if r.Meta.Vertical != "" {
		res.Values[domain.FieldIndustry] = provider.Observation{Value: r.Meta.Vertical, Confidence: 0.65}
	}
	if r.Meta.Country != "" {
		res.Values[domain.FieldCompanyHQCountry] = provider.Observation{Value: r.Meta.Country, Confidence: 0.70}
	}
	if r.Attributes.Employees > 0 {
		res.Values[domain.FieldEmployeeCount] = provider.Observation{Value: strconv.FormatInt(r.Attributes.Employees, 10), Confidence: 0.55}
	}
	if r.SalesRevenue > 0 {
		res.Values[domain.FieldCompanyRevenue] = provider.Observation{Value: strconv.FormatInt(r.SalesRevenue, 10), Confidence: 0.55}
	}
	return res, nil
}
