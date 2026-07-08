package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Enformion builds an adapter for the Enformion (EnformionGO) Contact Enrichment API (docs/03 §1).
//   - Endpoint: POST {base}/Contact/Enrich with JSON {FirstName,LastName,Phone,Email} — at least two
//     of name/phone/address/email are required (base default https://devapi.enformion.com)
//     [enformiongo.readme.io/reference/contact-enrichment].
//   - Auth: DUAL credential headers "galaxy-ap-name" + "galaxy-ap-password" (ADR-0024 Phase 4b) —
//     pool secret "<ap-name>:<ap-password>". The required ROUTING header "galaxy-search-type:
//     DevAPIContactEnrich" is static (not a secret) and set by Build.
//   - Input: first_name+last_name (+work_email as Email seed). Fills first/last name, mobile_phone
//     (top phone; type varies so mobile classification is approximate), phone_status (isConnected +
//     type → normalized), personal_email (first isBusiness=false email).
//   - Status: DEPRIORITIZED (ADR-0009) — US public-records / people-search provenance ("Large-Scale
//     Public Record Intelligence"); needs a compliance review before it can serve.
//   - Quirk: 200-with-error-body — every response carries isError + error.inputErrors[]; a no-match
//     is HTTP 200 with an absent/empty person (charge-on-match). Documented 400 body is literally {}.
//
// VERIFIED from the vendor's OpenAPI 3.1 + docs: endpoint, galaxy-* headers, person.* field paths.
// Exact field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Enformion(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://devapi.enformion.com"
	}
	return &provider.HTTPAdapter{
		NameV:   "enformion",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:           provider.AuthAPIKeyDualHeader,
			HeaderName:       "galaxy-ap-name",
			SecondHeaderName: "galaxy-ap-password",
			KeyPoolSelector:  "enformion:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldFirstName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldLastName, Cost: 3, ExpectedConfidence: 0.90},
			{Field: domain.FieldMobilePhone, Cost: 3, ExpectedConfidence: 0.65},
			{Field: domain.FieldPhoneStatus, Cost: 3, ExpectedConfidence: 0.80},
			{Field: domain.FieldPersonalEmail, Cost: 3, ExpectedConfidence: 0.70},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			payload := map[string]string{}
			putIf(payload, "FirstName", req.Known[domain.FieldFirstName])
			putIf(payload, "LastName", req.Known[domain.FieldLastName])
			putIf(payload, "Email", req.Known[domain.FieldWorkEmail])
			putIf(payload, "Phone", req.Known[domain.FieldMobilePhone])
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost,
				strings.TrimRight(base, "/")+"/Contact/Enrich", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			// Static routing header (vendor-required, not a credential).
			r.Header.Set("galaxy-search-type", "DevAPIContactEnrich")
			return r, nil
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Person struct {
					Name struct {
						FirstName string `json:"firstName"`
						LastName  string `json:"lastName"`
					} `json:"name"`
					Phones []struct {
						Number      string `json:"number"`
						Type        string `json:"type"`
						IsConnected bool   `json:"isConnected"`
					} `json:"phones"`
					Emails []struct {
						Email      string `json:"email"`
						IsBusiness bool   `json:"isBusiness"`
					} `json:"emails"`
				} `json:"person"`
				IsError bool `json:"isError"`
				Error   struct {
					InputErrors []string `json:"inputErrors"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// 200-with-error-body: isError + inputErrors (validation problems, not auth).
			if p.IsError {
				msg := strings.Join(p.Error.InputErrors, "; ")
				if msg == "" {
					msg = "provider returned isError with no detail"
				}
				return provider.Result{}, domain.NewProviderError("enformion", domain.ClassBadRequest, errors.New(msg))
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			put := func(f domain.Field, v string, conf float64) {
				if v != "" {
					res.Values[f] = provider.Observation{Value: v, Confidence: domain.Confidence(conf).Clamp()}
				}
			}
			put(domain.FieldFirstName, p.Person.Name.FirstName, 0.90)
			put(domain.FieldLastName, p.Person.Name.LastName, 0.90)
			if len(p.Person.Phones) > 0 {
				ph := p.Person.Phones[0]
				put(domain.FieldMobilePhone, ph.Number, 0.65)
				status := "invalid"
				if ph.IsConnected {
					status = phoneStatusFromType(ph.Type)
				}
				put(domain.FieldPhoneStatus, status, 0.80)
			}
			for _, e := range p.Person.Emails {
				if !e.IsBusiness && e.Email != "" {
					put(domain.FieldPersonalEmail, e.Email, 0.70)
					break
				}
			}
			return res, nil
		},
	}
}
