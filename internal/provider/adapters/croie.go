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

// CroIE builds an adapter for the Ireland CRO Open Services (CORE) company register (docs/03 §7).
//   - Endpoint: GET {base}/companies?company_name=&max=1&format=json  (base default
//     https://services.cro.ie/cws) [services.cro.ie/cws/help, live-verified].
//   - Auth: HTTP Basic (AuthBasic) — the "<slug>:default" pool must hold the full Basic credential
//     "<signup-email>:<api-key>" (egress base64-encodes the pool secret verbatim). The email is the
//     exact, case-sensitive signup address; the api_key is a GUID CRO emails back after signed T&Cs.
//   - Input: company_name. Fills company_name ([0].company_name), company_type ([0].comp_type_desc —
//     free-text legal form), company_founded_year (year of [0].company_reg_date; registration date
//     can diverge from true founding, ~0.55).
//   - Status: DEPRIORITIZED (ADR-0009) — official public-records register; manual signup gating.
//   - Quirk: the response is a bare JSON ARRAY; missing/invalid credentials return HTTP 401 with a
//     bare JSON STRING body (handled by the shared status map before Decode). CRO exposes no
//     employee/industry/revenue/contact fields, so only the three register fields are mapped.
//
// VERIFIED from CRO's own live API-help + sample code: endpoint, params, Basic auth form, field
// names. Exact values pinned UNVERIFIED until a live authorized call (see hunter.go).
func CroIE(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://services.cro.ie/cws"
	}
	return &provider.HTTPAdapter{
		NameV:   "cro-ie",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthBasic,
			KeyPoolSelector: "cro-ie:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyType, Cost: 0, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyFoundedYear, Cost: 0, ExpectedConfidence: 0.55},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/companies")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("company_name", req.Known[domain.FieldCompanyName])
			q.Set("max", "1")
			q.Set("format", "json")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var arr []struct {
				CompanyName    string `json:"company_name"`
				CompTypeDesc   string `json:"comp_type_desc"`
				CompanyRegDate string `json:"company_reg_date"`
			}
			if err := json.Unmarshal(body, &arr); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(arr) == 0 || arr[0].CompanyName == "" {
				return res, nil
			}
			c := arr[0]
			res.Values[domain.FieldCompanyName] = provider.Observation{Value: c.CompanyName, Confidence: 0.90}
			if c.CompTypeDesc != "" {
				res.Values[domain.FieldCompanyType] = provider.Observation{Value: c.CompTypeDesc, Confidence: 0.85}
			}
			if y := yearOf(c.CompanyRegDate); y != "" {
				res.Values[domain.FieldCompanyFoundedYear] = provider.Observation{Value: y, Confidence: 0.55}
			}
			return res, nil
		},
	}
}
