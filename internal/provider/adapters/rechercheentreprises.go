package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// RechercheEntreprises builds an adapter for the French government "Recherche d'entreprises" API
// (DINUM / SIRENE / RNE; docs/03 §7).
//   - Endpoint: GET {base}/search?q={name}&per_page=1  (base default
//     https://recherche-entreprises.api.gouv.fr) [recherche-entreprises.api.gouv.fr/docs, live-verified].
//   - Auth: NONE — fully open (AuthNone; egress passthrough). 7 req/s per IP.
//   - Input: company_name (SIREN also works as q). Fills: company_name (nom_complet), industry
//     (activite_principale — the official NAF/APE CODE like "84.11Z", not a label; documented),
//     company_type (nature_juridique — INSEE legal-category CODE like "5510"), company_founded_year
//     (date_creation), company_hq_city (siege.libelle_commune), company_hq_country (constant "FR" —
//     the registry covers French-registered entities only, cited), company_revenue (latest
//     finances.{year}.ca when filed; frequently null).
//   - NOT mapped (deliberate): tranche_effectif_salarie — an INSEE headcount BAND CODE whose band
//     semantics conflicted in research (needs a verified INSEE decode table before storing a range);
//     dirigeants[].nom/prenoms — company OFFICERS, not the enrichment subject (mapping them to
//     first/last name would attribute a director's identity to the lead).
//   - Quirk: no-match = HTTP 200 with results:[] (verified live); 400 carries a French-language
//     {"erreur": "..."} body; 429 has Retry-After.
//
// VERIFIED from the official openapi.json + live public calls (open-data API): endpoint, no-auth,
// field names, no-match envelope.
func RechercheEntreprises(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://recherche-entreprises.api.gouv.fr"
	}
	return &provider.HTTPAdapter{
		NameV:   "recherche-entreprises",
		BaseURL: base,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthNone},
		Caps: []provider.Capability{
			{Field: domain.FieldCompanyName, Cost: 0, ExpectedConfidence: 0.93},
			{Field: domain.FieldIndustry, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyType, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyFoundedYear, Cost: 0, ExpectedConfidence: 0.90},
			{Field: domain.FieldCompanyHQCity, Cost: 0, ExpectedConfidence: 0.93},
			{Field: domain.FieldCompanyHQCountry, Cost: 0, ExpectedConfidence: 0.95},
			{Field: domain.FieldCompanyRevenue, Cost: 0, ExpectedConfidence: 0.65},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/search")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("q", req.Known[domain.FieldCompanyName])
			q.Set("per_page", "1")
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Erreur  string `json:"erreur"`
				Results []struct {
					NomComplet         string `json:"nom_complet"`
					ActivitePrincipale string `json:"activite_principale"`
					NatureJuridique    string `json:"nature_juridique"`
					DateCreation       string `json:"date_creation"`
					Siege              struct {
						LibelleCommune string `json:"libelle_commune"`
					} `json:"siege"`
					// finances is keyed by accounting year: {"2023":{"ca":123,...},...}; often null.
					Finances map[string]struct {
						CA *int64 `json:"ca"`
					} `json:"finances"`
				} `json:"results"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// French-language in-body error (e.g. missing search param) — bad request, not a value.
			if p.Erreur != "" {
				return provider.Result{}, domain.NewProviderError("recherche-entreprises", domain.ClassBadRequest, errors.New(p.Erreur))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if len(p.Results) == 0 {
				return res, nil // results:[] = no match (verified live)
			}
			r := p.Results[0]
			put := func(f domain.Field, v string, c domain.Confidence) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: c}
				}
			}
			put(domain.FieldCompanyName, r.NomComplet, 0.93)
			put(domain.FieldIndustry, r.ActivitePrincipale, 0.90)
			put(domain.FieldCompanyType, r.NatureJuridique, 0.90)
			put(domain.FieldCompanyFoundedYear, yearOf(r.DateCreation), 0.90)
			put(domain.FieldCompanyHQCity, r.Siege.LibelleCommune, 0.93)
			// France-only registry: every returned entity is French-registered (cited coverage note).
			put(domain.FieldCompanyHQCountry, "FR", 0.95)
			// Latest filed annual revenue (chiffre d'affaires), if any year is present.
			latestYear, latestCA := "", int64(0)
			for year, f := range r.Finances {
				if f.CA != nil && year > latestYear {
					latestYear, latestCA = year, *f.CA
				}
			}
			if latestYear != "" {
				put(domain.FieldCompanyRevenue, itoa(latestCA), 0.65)
			}
			return res, nil
		},
	}
}
