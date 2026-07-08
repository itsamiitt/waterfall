package adapters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

// TestAsyncWave_SubmitPoll drives each ADR-0024 submit→poll / match→fetch adapter end-to-end: an
// httptest mux serves the oauth token (if any), the submit/match response (job token), and a
// TERMINAL poll/fetch body on the first read (so no sleep). Proves submit→token→poll→decode wiring,
// field maps, and egress key/oauth injection for the async finders/verifiers/firmographics.
func TestAsyncWave_SubmitPoll(t *testing.T) {
	cases := []struct {
		name         string
		newA         func(string, *http.Client) *provider.AsyncHTTPAdapter
		pool, secret string
		submitMethod string
		submitPath   string
		submitBody   string
		pollBody     string
		req          provider.Request
		want         map[domain.Field]string
	}{
		{
			name: "verifalia", newA: adapters.Verifalia, pool: "verifalia:default", secret: "u:p",
			submitMethod: "POST", submitPath: "/email-validations",
			submitBody: `{"overview":{"id":"job-1","status":"InProgress"}}`,
			pollBody:   `{"overview":{"id":"job-1","status":"Completed"},"entries":{"data":[{"emailAddress":"jane@acme.com","classification":"Deliverable"}]}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com"}},
			want:       map[domain.Field]string{domain.FieldEmailStatus: "deliverable", domain.FieldWorkEmail: "jane@acme.com"},
		},
		{
			name: "dropcontact", newA: adapters.Dropcontact, pool: "dropcontact:default", secret: "K",
			submitMethod: "POST", submitPath: "/enrich/all",
			submitBody: `{"error":false,"success":true,"request_id":"req-9"}`,
			pollBody:   `{"success":true,"data":[{"first_name":"Jane","last_name":"Doe","full_name":"Jane Doe","email":[{"email":"jane@acme.com","qualification":"nominative@pro"}],"company":"Acme","website":"acme.com"}]}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe", domain.FieldCompanyName: "Acme"}},
			want:       map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "nominative@pro", domain.FieldCompanyDomain: "acme.com"},
		},
		{
			name: "icypeas", newA: adapters.Icypeas, pool: "icypeas:default", secret: "K",
			submitMethod: "POST", submitPath: "/email-search",
			submitBody: `{"success":true,"item":{"status":"NONE","_id":"itm-3"}}`,
			pollBody:   `{"success":true,"items":[{"status":"DEBITED","results":{"firstname":"Jane","lastname":"Doe","fullname":"Jane Doe","emails":[{"email":"jane@acme.com","certainty":"ultra_sure"}]}}]}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe", domain.FieldCompanyDomain: "acme.com"}},
			want:       map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "ultra_sure", domain.FieldFullName: "Jane Doe"},
		},
		{
			name: "enrow", newA: adapters.Enrow, pool: "enrow:default", secret: "K",
			submitMethod: "POST", submitPath: "/email/find/single",
			submitBody: `{"message":"Single search operating","id":"srch-7","credits_used":1}`,
			pollBody:   `{"email":"jane@acme.com","qualification":"valid","info":{"company_domain":"acme.com","firstname":"Jane","lastname":"Doe","fullname":"Jane Doe"}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFullName: "Jane Doe", domain.FieldCompanyDomain: "acme.com"}},
			want:       map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "valid", domain.FieldCompanyDomain: "acme.com"},
		},
		{
			name: "snov", newA: adapters.Snov, pool: "snov:default", secret: "cid:csec",
			submitMethod: "POST", submitPath: "/v2/emails-by-domain-by-name/start",
			submitBody: `{"success":true,"data":{"task_hash":"th-1"}}`,
			pollBody:   `{"status":"completed","data":[{"people":"Jane Doe","result":[{"email":"jane@acme.com","smtp_status":"valid"}]}]}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe", domain.FieldCompanyDomain: "acme.com"}},
			want:       map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "valid", domain.FieldFullName: "Jane Doe"},
		},
		{
			name: "explorium", newA: adapters.Explorium, pool: "explorium:default", secret: "K",
			submitMethod: "POST", submitPath: "/v1/businesses/match",
			submitBody: `{"response_context":{"request_status":"success"},"matched_businesses":[{"business_id":"bid-1"}]}`,
			pollBody:   `{"response_context":{"request_status":"success"},"data":{"name":"Acme","website":"https://acme.com","naics":"541511","sic_code":"7371","country_name":"United States","city_name":"San Francisco","linkedin_industry_category":"Information Technology","linkedin_profile":"https://linkedin.com/company/acme","number_of_employees_range":{"min":100,"max":500},"yearly_revenue_range":{"min":10000000,"max":50000000}}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldCompanyName: "Acme", domain.FieldCompanyDomain: "acme.com"}},
			want: map[domain.Field]string{
				domain.FieldCompanyName: "Acme", domain.FieldCompanyDomain: "acme.com", domain.FieldNAICS: "541511", domain.FieldSIC: "7371",
				domain.FieldCompanyHQCountry: "United States", domain.FieldEmployeeCount: "100-500", domain.FieldCompanyRevenue: "10000000-50000000",
			},
		},
		{
			name: "bettercontact", newA: adapters.BetterContact, pool: "bettercontact:default", secret: "K",
			submitMethod: "POST", submitPath: "/async",
			submitBody: `{"success":true,"id":"req-bc","message":"Processing..."}`,
			pollBody:   `{"id":"req-bc","status":"terminated","data":[{"contact_first_name":"Jane","contact_last_name":"Doe","contact_email_address":"jane@acme.com","contact_email_address_status":"deliverable","contact_job_title":"VP Sales"}]}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe", domain.FieldCompanyDomain: "acme.com"}},
			want:       map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "deliverable", domain.FieldJobTitle: "VP Sales"},
		},
		{
			name: "fullenrich", newA: adapters.FullEnrich, pool: "fullenrich:default", secret: "K",
			submitMethod: "POST", submitPath: "/contact/enrich/bulk",
			submitBody: `{"enrichment_id":"enr-1"}`,
			pollBody:   `{"id":"enr-1","status":"FINISHED","data":[{"contact_info":{"most_probable_work_email":{"email":"jane@acme.com","status":"DELIVERABLE"},"most_probable_phone":{"number":"+15555550100"}},"profile":{"first_name":"Jane","last_name":"Doe","full_name":"Jane Doe","employment":{"current":{"title":"VP Sales","company":{"name":"Acme","domain":"acme.com","headcount":250,"year_founded":2010}}}}}]}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe", domain.FieldCompanyDomain: "acme.com"}},
			want: map[domain.Field]string{
				domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "DELIVERABLE", domain.FieldMobilePhone: "+15555550100",
				domain.FieldJobTitle: "VP Sales", domain.FieldCompanyName: "Acme", domain.FieldEmployeeCount: "250", domain.FieldCompanyFoundedYear: "2010",
			},
		},
		{
			name: "wiza", newA: adapters.Wiza, pool: "wiza:default", secret: "K",
			submitMethod: "POST", submitPath: "/api/individual_reveals",
			submitBody: `{"status":{"code":200},"data":{"id":32,"status":"queued"}}`,
			pollBody:   `{"data":{"id":32,"status":"finished","name":"Jane Doe","title":"VP Sales","email":"jane@acme.com","email_status":"valid","mobile_phone":"+15555550100","phone_status":"found","company":"Acme","company_domain":"acme.com","company_size":21,"company_country":"Canada","company_founded":2017}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldLinkedInURL: "https://www.linkedin.com/in/janedoe"}},
			want: map[domain.Field]string{
				domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "valid", domain.FieldMobilePhone: "+15555550100",
				domain.FieldFullName: "Jane Doe", domain.FieldCompanyName: "Acme", domain.FieldEmployeeCount: "21", domain.FieldCompanyFoundedYear: "2017",
			},
		},
		{
			name: "rocketreach", newA: adapters.RocketReach, pool: "rocketreach:default", secret: "K",
			submitMethod: "GET", submitPath: "/person/lookup",
			submitBody: `{"id":5244,"status":"searching"}`,
			pollBody:   `[{"id":5244,"status":"complete","name":"Jane Doe","current_title":"VP Sales","current_employer":"Acme","linkedin_url":"https://www.linkedin.com/in/janedoe","emails":[{"email":"jane@acme.com","type":"professional","grade":"A"},{"email":"jane@gmail.com","type":"personal","grade":"B"}],"phones":[{"number":"+15555550100","type":"mobile"}]}]`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFullName: "Jane Doe", domain.FieldCompanyName: "Acme"}},
			want: map[domain.Field]string{
				domain.FieldWorkEmail: "jane@acme.com", domain.FieldPersonalEmail: "jane@gmail.com", domain.FieldEmailStatus: "A",
				domain.FieldMobilePhone: "+15555550100", domain.FieldJobTitle: "VP Sales", domain.FieldCompanyName: "Acme", domain.FieldLinkedInURL: "https://www.linkedin.com/in/janedoe",
			},
		},
		{
			name: "demandbase", newA: adapters.Demandbase, pool: "demandbase:default", secret: "cid:csec",
			submitMethod: "POST", submitPath: "/match",
			submitBody: `{"matches":[{"companyMatches":[{"company":{"companyId":"c-1"}}]}]}`,
			pollBody:   `{"companyName":"Acme","websites":["https://acme.com"],"industry":"Software","employeeCount":1200,"revenue":250,"naics":"541511","sic":"7372","address":{"country":"United States","city":"San Francisco"}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"}},
			want: map[domain.Field]string{
				domain.FieldCompanyName: "Acme", domain.FieldCompanyDomain: "acme.com", domain.FieldIndustry: "Software",
				domain.FieldEmployeeCount: "1200", domain.FieldNAICS: "541511", domain.FieldSIC: "7372", domain.FieldCompanyHQCountry: "United States",
			},
		},
		{
			name: "endole", newA: adapters.Endole, pool: "endole:default", secret: "app:key",
			submitMethod: "GET", submitPath: "/search/companies",
			submitBody: `{"items":[{"company_number":"00445790"}]}`,
			pollBody:   `{"company_number":"00445790","company_name":"TESCO PLC","type":"plc","date_of_creation":"1947-11-27","sic_codes":["47110"],"registered_office_address":{"locality":"Welwyn Garden City","country":"United Kingdom"}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldCompanyName: "Tesco"}},
			want: map[domain.Field]string{
				domain.FieldCompanyName: "TESCO PLC", domain.FieldCompanyType: "plc", domain.FieldCompanyFoundedYear: "1947",
				domain.FieldSIC: "47110", domain.FieldCompanyHQCountry: "United Kingdom", domain.FieldCompanyHQCity: "Welwyn Garden City",
			},
		},
		// Wave 9 — additional async providers (net-new + revisited deferrals).
		{
			name: "surfe", newA: adapters.Surfe, pool: "surfe:default", secret: "K",
			submitMethod: "POST", submitPath: "/v2/people/enrich",
			submitBody: `{"enrichmentID":"enr-1","status":"IN_PROGRESS"}`,
			pollBody:   `{"status":"COMPLETED","people":[{"firstName":"Jane","lastName":"Doe","companyName":"Acme","companyDomain":"acme.com","linkedInUrl":"https://www.linkedin.com/in/janedoe","jobTitle":"VP Sales","seniorities":["Manager"],"departments":["Sales"],"emails":[{"email":"jane@acme.com","validationStatus":"VALID"}],"mobilePhones":[{"mobilePhone":"+15555550100"}]}]}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe", domain.FieldCompanyDomain: "acme.com"}},
			want: map[domain.Field]string{
				domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "VALID", domain.FieldMobilePhone: "+15555550100",
				domain.FieldJobTitle: "VP Sales", domain.FieldSeniority: "Manager", domain.FieldDepartment: "Sales", domain.FieldLinkedInURL: "https://www.linkedin.com/in/janedoe",
			},
		},
		{
			name: "lemlist", newA: adapters.Lemlist, pool: "lemlist:default", secret: ":K",
			submitMethod: "POST", submitPath: "/enrich",
			submitBody: `{"id":"enr_ABC"}`,
			pollBody:   `{"enrichmentStatus":"done","data":{"email":{"email":"jane@acme.com","notFound":false}}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe", domain.FieldCompanyDomain: "acme.com"}},
			want:       map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "deliverable"},
		},
		{
			name: "companies-house", newA: adapters.CompaniesHouse, pool: "companies-house:default", secret: "K:",
			submitMethod: "GET", submitPath: "/search/companies",
			submitBody: `{"items":[{"company_number":"00000006"}]}`,
			pollBody:   `{"company_name":"EXAMPLE TRADING LIMITED","type":"ltd","date_of_creation":"1872-06-05","sic_codes":["82990"],"registered_office_address":{"locality":"London","country":"England"}}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldCompanyName: "Example Trading"}},
			want: map[domain.Field]string{
				domain.FieldCompanyName: "EXAMPLE TRADING LIMITED", domain.FieldCompanyType: "ltd", domain.FieldCompanyFoundedYear: "1872",
				domain.FieldSIC: "82990", domain.FieldCompanyHQCountry: "England", domain.FieldCompanyHQCity: "London",
			},
		},
		// Wave 11 — official open-data registries (AuthNone: no credential, egress passthrough).
		{
			name: "brreg", newA: adapters.Brreg, pool: "brreg:default", secret: "UNUSED",
			submitMethod: "GET", submitPath: "/enheter",
			submitBody: `{"_embedded":{"enheter":[{"organisasjonsnummer":"923609016","navn":"EQUINOR ASA"}]},"page":{"totalElements":1}}`,
			pollBody:   `{"organisasjonsnummer":"923609016","navn":"EQUINOR ASA","organisasjonsform":{"kode":"ASA","beskrivelse":"Allmennaksjeselskap"},"hjemmeside":"www.equinor.com","telefon":"51 99 00 00","naeringskode1":{"kode":"06.100","beskrivelse":"Utvinning av råolje"},"antallAnsatte":21467,"stiftelsesdato":"1972-09-18","forretningsadresse":{"adresse":["Forusbeen 50"],"poststed":"STAVANGER","landkode":"NO","land":"Norge"},"konkurs":false}`,
			req:        provider.Request{Known: map[domain.Field]string{domain.FieldCompanyName: "Equinor"}},
			want: map[domain.Field]string{
				domain.FieldCompanyName: "EQUINOR ASA", domain.FieldCompanyType: "ASA", domain.FieldCompanyFoundedYear: "1972",
				domain.FieldEmployeeCount: "21467", domain.FieldIndustry: "Utvinning av råolje", domain.FieldCompanyDomain: "equinor.com",
				domain.FieldCompanyHQCountry: "NO", domain.FieldCompanyHQCity: "STAVANGER", domain.FieldCompanyPhone: "51 99 00 00",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "token") || strings.Contains(r.URL.Path, "oauth"):
					_, _ = w.Write([]byte(`{"access_token":"TOK","token_type":"Bearer","expires_in":3600}`))
				case r.Method == tc.submitMethod && r.URL.Path == tc.submitPath:
					_, _ = w.Write([]byte(tc.submitBody))
				default:
					_, _ = w.Write([]byte(tc.pollBody))
				}
			}))
			defer srv.Close()

			a := tc.newA(srv.URL, clientWith(srv, tc.pool, tc.secret))
			res, err := a.Fetch(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("%s fetch: %v", tc.name, err)
			}
			for f, v := range tc.want {
				got := res.Values[f]
				if got.Value != v {
					t.Errorf("%s: %s = %q, want %q", tc.name, f, got.Value, v)
				}
				if got.Confidence <= 0 {
					t.Errorf("%s: %s has non-positive confidence %v", tc.name, f, got.Confidence)
				}
			}
		})
	}
}
