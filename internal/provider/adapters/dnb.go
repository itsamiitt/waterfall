package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// DNB builds an adapter for the Dun & Bradstreet Direct+ Data Blocks API (docs/03 §7) — the
// authoritative company registry (genuine 9-digit DUNS). It is the first real consumer of the
// ADR-0024 async foundation, exercising all three phases at once:
//   - match→fetch (Phase 3): Submit resolves the identity via GET /v1/match/cleanseMatch (by name +
//     country + domain) → the top candidate's DUNS; Poll then fetches the data block by that DUNS
//     via GET /v1/data/duns/{duns}?blockIDs=companyinfo_L2_v1. Decode returns done=true (single fetch).
//   - oauth2-cc (Phase 2): Auth scheme is oauth2-cc; the egress AuthInjector exchanges the pool
//     secret ("consumerKey:consumerSecret") at {base}/v2/token for a Bearer token (cached) and
//     injects it on BOTH round-trips. The adapter holds no secret.
//   - per-adapter budget (Phase 1): a 30s bounded CallPolicy (match+fetch+token exchange), single
//     attempt (PolicyOverrider via AsyncHTTPAdapter).
//
// VERIFIED from docs (+ hdr1001 cURL gist, apis.io OpenAPI mirror): token/match/data endpoints, the
// {transactionDetail,inquiryDetail,organization} wrapper, and organization fields duns/primaryName/
// primaryAddress/numberOfEmployees/financials.yearlyRevenue/primaryIndustryCode/websiteAddress. The
// docs host 403s automated fetches so leaf paths are reconstructed from documented samples — pinned
// UNVERIFIED until a live authorized call (see hunter.go). startDate (founded year), businessEntityType
// (company_type), and the NAICS array selector were not confidently confirmed, so are not mapped.
func DNB(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://plus.dnb.com"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:   "dnb",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthOAuth2CC,
			KeyPoolSelector: "dnb:default",
			TokenURL:        base + "/v2/token", // same host as data/match — one SSRF allow-list entry
		},
		Policy: provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		Caps: []provider.Capability{
			{Field: domain.FieldDUNS, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyName, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 5, ExpectedConfidence: 0.70},
			{Field: domain.FieldCompanyHQCountry, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyHQCity, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldCompanyRevenue, Cost: 5, ExpectedConfidence: 0.65},
			{Field: domain.FieldSIC, Cost: 5, ExpectedConfidence: 0.85},
			{Field: domain.FieldIndustry, Cost: 5, ExpectedConfidence: 0.85},
		},
		// Submit: cleanseMatch by the identity keys we hold.
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base + "/v1/match/cleanseMatch")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			setIf(q, "name", req.Known[domain.FieldCompanyName])
			setIf(q, "countryISOAlpha2Code", req.Known[domain.FieldCompanyHQCountry])
			setIf(q, "url", req.Known[domain.FieldCompanyDomain])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		// ParseSubmit: take the top match candidate's DUNS; no candidate = NOT_FOUND (refund + failover).
		ParseSubmit: func(body []byte) (string, error) {
			var m struct {
				MatchCandidates []struct {
					Organization struct {
						DUNS string `json:"duns"`
					} `json:"organization"`
				} `json:"matchCandidates"`
			}
			if err := json.Unmarshal(body, &m); err != nil {
				return "", err
			}
			if len(m.MatchCandidates) == 0 || m.MatchCandidates[0].Organization.DUNS == "" {
				return "", domain.NewProviderError("dnb", domain.ClassNotFound, errors.New("no match candidates"))
			}
			return m.MatchCandidates[0].Organization.DUNS, nil
		},
		// Poll: fetch the company-info data block by DUNS (single call — Decode returns done).
		Poll: func(ctx context.Context, base, duns string) (*http.Request, error) {
			u := base + "/v1/data/duns/" + url.PathEscape(duns) + "?blockIDs=companyinfo_L2_v1"
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		},
		Decode: decodeDNB,
	}
}

func decodeDNB(body []byte) (provider.Result, bool, error) {
	var p struct {
		Organization struct {
			DUNS           string `json:"duns"`
			PrimaryName    string `json:"primaryName"`
			WebsiteAddress []struct {
				DomainName string `json:"domainName"`
			} `json:"websiteAddress"`
			PrimaryAddress struct {
				AddressCountry struct {
					ISOAlpha2Code string `json:"isoAlpha2Code"`
				} `json:"addressCountry"`
				AddressLocality struct {
					Name string `json:"name"`
				} `json:"addressLocality"`
			} `json:"primaryAddress"`
			NumberOfEmployees []struct {
				Value int64 `json:"value"`
			} `json:"numberOfEmployees"`
			Financials []struct {
				YearlyRevenue []struct {
					Value float64 `json:"value"`
				} `json:"yearlyRevenue"`
			} `json:"financials"`
			PrimaryIndustryCode struct {
				USSicV4            string `json:"usSicV4"`
				USSicV4Description string `json:"usSicV4Description"`
			} `json:"primaryIndustryCode"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, false, err
	}
	o := p.Organization
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	put := func(f domain.Field, v string, c domain.Confidence) {
		if v != "" {
			res.Values[f] = provider.Observation{Value: v, Confidence: c}
		}
	}
	put(domain.FieldDUNS, o.DUNS, 0.85)
	put(domain.FieldCompanyName, o.PrimaryName, 0.85)
	put(domain.FieldCompanyHQCountry, o.PrimaryAddress.AddressCountry.ISOAlpha2Code, 0.85)
	put(domain.FieldCompanyHQCity, o.PrimaryAddress.AddressLocality.Name, 0.85)
	put(domain.FieldSIC, o.PrimaryIndustryCode.USSicV4, 0.85)
	put(domain.FieldIndustry, o.PrimaryIndustryCode.USSicV4Description, 0.85)
	if len(o.WebsiteAddress) > 0 {
		put(domain.FieldCompanyDomain, o.WebsiteAddress[0].DomainName, 0.70)
	}
	if len(o.NumberOfEmployees) > 0 && o.NumberOfEmployees[0].Value > 0 {
		put(domain.FieldEmployeeCount, itoa(o.NumberOfEmployees[0].Value), 0.65)
	}
	if len(o.Financials) > 0 && len(o.Financials[0].YearlyRevenue) > 0 && o.Financials[0].YearlyRevenue[0].Value > 0 {
		put(domain.FieldCompanyRevenue, itoa(int64(o.Financials[0].YearlyRevenue[0].Value)), 0.65)
	}
	// match→fetch: the data-block response is terminal (no polling).
	return res, true, nil
}
