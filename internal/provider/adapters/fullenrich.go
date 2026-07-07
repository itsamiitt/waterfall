package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// FullEnrich builds an async adapter for the FullEnrich v2 waterfall enrichment API (docs/03 §7).
//   - Submit: POST {base}/contact/enrich/bulk with {"name":…,"data":[{first_name,last_name,domain,
//     company_name,linkedin_url,enrich_fields:[…]}]} → enrichment_id.
//   - Poll: GET {base}/contact/enrich/bulk/{id} until status=="FINISHED" (CREATED/IN_PROGRESS
//     pending; CREDITS_INSUFFICIENT → QUOTA; CANCELED/RATE_LIMIT/UNKNOWN → PROVIDER_DOWN).
//   - Auth: Bearer token, injected at egress.
//   - base default https://app.fullenrich.com/api/v2 [docs.fullenrich.com].
//
// VERIFIED from docs + a verbatim example: endpoints, Bearer, enrichment_id, nested
// data[].contact_info.* and data[].profile.* fields. Field names pinned UNVERIFIED (see hunter.go).
func FullEnrich(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://app.fullenrich.com/api/v2"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "fullenrich",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: "fullenrich:default"},
		Policy:       provider.CallPolicy{Timeout: 120 * time.Second, MaxAttempts: 1},
		PollInterval: 5 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldWorkEmail, Cost: 12, ExpectedConfidence: 0.90},
			{Field: domain.FieldEmailStatus, Cost: 12, ExpectedConfidence: 0.90},
			{Field: domain.FieldPersonalEmail, Cost: 12, ExpectedConfidence: 0.85},
			{Field: domain.FieldMobilePhone, Cost: 12, ExpectedConfidence: 0.85},
			{Field: domain.FieldLinkedInURL, Cost: 12, ExpectedConfidence: 0.90},
			{Field: domain.FieldJobTitle, Cost: 12, ExpectedConfidence: 0.85},
			{Field: domain.FieldFirstName, Cost: 12, ExpectedConfidence: 0.95},
			{Field: domain.FieldLastName, Cost: 12, ExpectedConfidence: 0.95},
			{Field: domain.FieldFullName, Cost: 12, ExpectedConfidence: 0.95},
			{Field: domain.FieldCompanyName, Cost: 12, ExpectedConfidence: 0.85},
			{Field: domain.FieldCompanyDomain, Cost: 12, ExpectedConfidence: 0.85},
			{Field: domain.FieldEmployeeCount, Cost: 12, ExpectedConfidence: 0.80},
			{Field: domain.FieldIndustry, Cost: 12, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyType, Cost: 12, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyFoundedYear, Cost: 12, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCountry, Cost: 12, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyHQCity, Cost: 12, ExpectedConfidence: 0.80},
			{Field: domain.FieldCompanyLinkedInURL, Cost: 12, ExpectedConfidence: 0.80},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			contact := map[string]any{"enrich_fields": []string{"contact.work_emails", "contact.personal_emails", "contact.phones"}}
			setStr := func(k, v string) {
				if v != "" {
					contact[k] = v
				}
			}
			setStr("first_name", req.Known[domain.FieldFirstName])
			setStr("last_name", req.Known[domain.FieldLastName])
			setStr("domain", req.Known[domain.FieldCompanyDomain])
			setStr("company_name", req.Known[domain.FieldCompanyName])
			setStr("linkedin_url", req.Known[domain.FieldLinkedInURL])
			body := map[string]any{"name": "waterfall enrichment", "data": []map[string]any{contact}}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/contact/enrich/bulk", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: func(body []byte) (string, error) {
			var p struct {
				EnrichmentID string `json:"enrichment_id"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return "", err
			}
			if p.EnrichmentID == "" {
				return "", domain.NewProviderError("fullenrich", domain.ClassTransient, errNoJobID)
			}
			return p.EnrichmentID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/contact/enrich/bulk/"+token, nil)
		},
		Decode: decodeFullEnrich,
	}
}

func decodeFullEnrich(body []byte) (provider.Result, bool, error) {
	var p struct {
		Status string `json:"status"`
		Data   []struct {
			ContactInfo struct {
				WorkEmail struct {
					Email  string `json:"email"`
					Status string `json:"status"`
				} `json:"most_probable_work_email"`
				PersonalEmail struct {
					Email string `json:"email"`
				} `json:"most_probable_personal_email"`
				Phone struct {
					Number string `json:"number"`
				} `json:"most_probable_phone"`
			} `json:"contact_info"`
			Profile struct {
				FirstName      string `json:"first_name"`
				LastName       string `json:"last_name"`
				FullName       string `json:"full_name"`
				SocialProfiles struct {
					ProfessionalNetwork struct {
						URL string `json:"url"`
					} `json:"professional_network"`
				} `json:"social_profiles"`
				Employment struct {
					Current struct {
						Title   string `json:"title"`
						Company struct {
							Name        string `json:"name"`
							Domain      string `json:"domain"`
							Headcount   int64  `json:"headcount"`
							CompanyType string `json:"company_type"`
							YearFounded int64  `json:"year_founded"`
							Industry    struct {
								MainIndustry string `json:"main_industry"`
							} `json:"industry"`
							Locations struct {
								Headquarters struct {
									City    string `json:"city"`
									Country string `json:"country"`
								} `json:"headquarters"`
							} `json:"locations"`
							SocialProfiles struct {
								ProfessionalNetwork struct {
									URL string `json:"url"`
								} `json:"professional_network"`
							} `json:"social_profiles"`
						} `json:"company"`
					} `json:"current"`
				} `json:"employment"`
			} `json:"profile"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, false, err
	}
	switch p.Status {
	case "FINISHED":
		// terminal — map below
	case "CREATED", "IN_PROGRESS", "":
		return provider.Result{}, false, nil
	case "CREDITS_INSUFFICIENT":
		return provider.Result{}, false, domain.NewProviderError("fullenrich", domain.ClassQuota, errResultsGone)
	default: // CANCELED / RATE_LIMIT / UNKNOWN
		return provider.Result{}, false, domain.NewProviderError("fullenrich", domain.ClassProviderDown, errResultsGone)
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if len(p.Data) == 0 {
		return res, true, nil
	}
	d := p.Data[0]
	co := d.Profile.Employment.Current.Company
	put := func(f domain.Field, v string, c domain.Confidence) {
		if v != "" {
			res.Values[f] = provider.Observation{Value: v, Confidence: c}
		}
	}
	put(domain.FieldWorkEmail, d.ContactInfo.WorkEmail.Email, 0.90)
	put(domain.FieldEmailStatus, d.ContactInfo.WorkEmail.Status, 0.90)
	put(domain.FieldPersonalEmail, d.ContactInfo.PersonalEmail.Email, 0.85)
	put(domain.FieldMobilePhone, d.ContactInfo.Phone.Number, 0.85)
	put(domain.FieldLinkedInURL, d.Profile.SocialProfiles.ProfessionalNetwork.URL, 0.90)
	put(domain.FieldJobTitle, d.Profile.Employment.Current.Title, 0.85)
	put(domain.FieldFirstName, d.Profile.FirstName, 0.95)
	put(domain.FieldLastName, d.Profile.LastName, 0.95)
	put(domain.FieldFullName, d.Profile.FullName, 0.95)
	put(domain.FieldCompanyName, co.Name, 0.85)
	put(domain.FieldCompanyDomain, co.Domain, 0.85)
	put(domain.FieldIndustry, co.Industry.MainIndustry, 0.80)
	put(domain.FieldCompanyType, co.CompanyType, 0.80)
	put(domain.FieldCompanyHQCountry, co.Locations.Headquarters.Country, 0.80)
	put(domain.FieldCompanyHQCity, co.Locations.Headquarters.City, 0.80)
	put(domain.FieldCompanyLinkedInURL, co.SocialProfiles.ProfessionalNetwork.URL, 0.80)
	if co.Headcount > 0 {
		put(domain.FieldEmployeeCount, itoa(co.Headcount), 0.80)
	}
	if co.YearFounded > 0 {
		put(domain.FieldCompanyFoundedYear, itoa(co.YearFounded), 0.80)
	}
	return res, true, nil
}
