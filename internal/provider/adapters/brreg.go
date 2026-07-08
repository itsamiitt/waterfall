package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Brreg builds a match→fetch adapter for Norway's Brønnøysund Enhetsregisteret (docs/03 §7).
//   - Match: GET {base}/enheter?navn={company_name}&size=1 → _embedded.enheter[0].organisasjonsnummer.
//     Zero matches = HTTP 200 with the _embedded key ENTIRELY ABSENT (verified live) → NOT_FOUND.
//   - Fetch: GET {base}/enheter/{orgnr} → the entity (terminal, single fetch).
//   - Auth: NONE — fully open official registry (NLOD 2.0), no key, no registration (AuthNone; the
//     egress passes the request through without a key lease). First registered AuthNone consumer.
//   - base default https://data.brreg.no/enhetsregisteret/api [data.brreg.no docs, live-verified].
//   - Fills company_name (navn), employee_count (antallAnsatte), industry (naeringskode1.beskrivelse,
//     Norwegian text; .kode is NACE-based Norwegian SN — NOT NAICS, so not mapped there),
//     company_domain (hjemmeside, normalized via bareDomain), company_founded_year (stiftelsesdato —
//     not the later registreringsdato), company_hq_country (forretningsadresse.landkode),
//     company_hq_city (.poststed), company_type (organisasjonsform.kode: AS|ASA|ENK|NUF|…),
//     company_phone (telefon).
//   - Quirk: 410 Gone = entity removed for legal reasons (permanent no-match; consumers must purge
//     caches). NO-region coverage only.
//
// VERIFIED from official docs + live public calls: endpoints, no-auth, Norwegian field names,
// zero-match envelope. (Public open-data API — live reads are authorized by design.)
func Brreg(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://data.brreg.no/enhetsregisteret/api"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "brreg",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthNone},
		Policy:       provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		PollInterval: 1 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 0, ExpectedConfidence: 0.95},
			{Field: domain.FieldEmployeeCount, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldIndustry, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyDomain, Cost: 0, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyFoundedYear, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCountry, Cost: 0, ExpectedConfidence: 0.95},
			{Field: domain.FieldCompanyHQCity, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyType, Cost: 0, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyPhone, Cost: 0, ExpectedConfidence: 0.80},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/enheter")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("navn", req.Known[domain.FieldCompanyName])
			q.Set("size", "1")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				Embedded struct {
					Enheter []struct {
						Organisasjonsnummer string `json:"organisasjonsnummer"`
					} `json:"enheter"`
				} `json:"_embedded"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			// Zero matches: _embedded absent entirely (HTTP 200) — a no-match, not a parse error.
			if len(p.Embedded.Enheter) == 0 || p.Embedded.Enheter[0].Organisasjonsnummer == "" {
				return "", domain.NewProviderError("brreg", domain.ClassNotFound, errNoMatch)
			}
			return p.Embedded.Enheter[0].Organisasjonsnummer, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet,
				strings.TrimRight(base, "/")+"/enheter/"+url.PathEscape(token), nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var p struct {
				Navn              string `json:"navn"`
				Hjemmeside        string `json:"hjemmeside"`
				Telefon           string `json:"telefon"`
				AntallAnsatte     int64  `json:"antallAnsatte"`
				Stiftelsesdato    string `json:"stiftelsesdato"`
				Organisasjonsform struct {
					Kode string `json:"kode"`
				} `json:"organisasjonsform"`
				Naeringskode1 struct {
					Beskrivelse string `json:"beskrivelse"`
				} `json:"naeringskode1"`
				Forretningsadresse struct {
					Poststed string `json:"poststed"`
					Landkode string `json:"landkode"`
				} `json:"forretningsadresse"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, false, err
			}
			// Single fetch of the entity record — always terminal.
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, p.Navn, 0.95)
			if p.AntallAnsatte > 0 {
				put(domain.FieldEmployeeCount, itoa(p.AntallAnsatte), 0.90)
			}
			put(domain.FieldIndustry, p.Naeringskode1.Beskrivelse, 0.90)
			put(domain.FieldCompanyDomain, bareDomain(p.Hjemmeside), 0.80)
			put(domain.FieldCompanyFoundedYear, yearOf(p.Stiftelsesdato), 0.90)
			put(domain.FieldCompanyHQCountry, p.Forretningsadresse.Landkode, 0.95)
			put(domain.FieldCompanyHQCity, p.Forretningsadresse.Poststed, 0.90)
			put(domain.FieldCompanyType, p.Organisasjonsform.Kode, 0.85)
			put(domain.FieldCompanyPhone, p.Telefon, 0.80)
			return res, true, nil
		},
	}
}
