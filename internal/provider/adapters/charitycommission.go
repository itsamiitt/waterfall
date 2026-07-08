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

// CharityCommissionUK builds an adapter for the UK Charity Commission (CCEW) Register of Charities
// API (docs/03 §7).
//   - Endpoint: GET {base}/searchCharityName/{name}  (base default
//     https://api.charitycommission.gov.uk/register/api) [register-of-charities.charitycommission.gov.uk].
//   - Auth: subscription key in the "Ocp-Apim-Subscription-Key" HEADER (Azure APIM; free portal
//     signup), injected at egress (AuthAPIKeyHeader).
//   - Input: company_name. Fills company_name ([0].charity_name), company_founded_year (year of
//     [0].date_of_registration — the registration date, a proxy for founding, ~0.4) and a constant
//     company_type="charity" (every result IS a registered charity).
//   - Status: DEPRIORITIZED (ADR-0009) — England & Wales charity register (public-records; also bulk
//     downloadable), covers only the narrow slice of input companies that are registered charities.
//
// NOTE ON SCOPE: this uses ONLY the officially-published searchCharityName response schema (snake_case
// organisation_number/reg_charity_number/charity_name/reg_status/date_of_registration). The
// "detailed" fetch endpoints (contact/financial/employee fields) sit behind a portal login and their
// field names were only available from an unofficial client with mismatched routes — so those fields
// are intentionally NOT mapped rather than shipped as fabricated paths. Scotland/NI charities use
// separate regulators (OSCR/CCNI), not this API.
//
// VERIFIED from official docs/PDF operation list: endpoint route, search response snake_case fields.
// The Ocp-Apim-Subscription-Key header is a high-confidence Azure-APIM inference (see docs/03).
func CharityCommissionUK(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.charitycommission.gov.uk/register/api"
	}
	return &provider.HTTPAdapter{
		NameV:   "charity-commission-uk",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "Ocp-Apim-Subscription-Key",
			KeyPoolSelector: "charity-commission-uk:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 0, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyFoundedYear, Cost: 0, ExpectedConfidence: 0.40},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u := strings.TrimRight(base, "/") + "/searchCharityName/" + url.PathEscape(req.Known[domain.FieldCompanyName])
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var arr []struct {
				CharityName        string `json:"charity_name"`
				RegStatus          string `json:"reg_status"`
				DateOfRegistration string `json:"date_of_registration"`
			}
			if err := json.Unmarshal(body, &arr); err != nil {
				return provider.Result{}, err
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(arr) == 0 || arr[0].CharityName == "" {
				return res, nil
			}
			c := arr[0]
			res.Values[domain.FieldCompanyName] = provider.Observation{Value: c.CharityName, Confidence: 0.80}
			// Every result on this register is a charity — a safe, high-confidence constant.
			res.Values[domain.FieldCompanyType] = provider.Observation{Value: "charity", Confidence: 0.90}
			if y := yearOf(c.DateOfRegistration); y != "" {
				res.Values[domain.FieldCompanyFoundedYear] = provider.Observation{Value: y, Confidence: 0.40}
			}
			return res, nil
		},
	}
}
