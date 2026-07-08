package adapters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

// TestWave0_InBodyErrorClassified proves the HTTPAdapter preserves an adapter-classified error
// returned from Decode (the 200-with-error-body pattern): ZeroBounce signals a bad key / exhausted
// credits as HTTP 200 + {"error":...}, which must surface as AUTH (not a generic BAD_REQUEST) so
// the engine disables/alerts the key rather than treating it as no-data.
func TestWave0_InBodyErrorClassified(t *testing.T) {
	cases := []struct {
		name string
		newA func(string, *http.Client) *provider.HTTPAdapter
		pool string
		body string
		want domain.ErrorClass
	}{
		{"zerobounce-auth", adapters.ZeroBounce, "zerobounce:default",
			`{"error":"Invalid API Key or your account ran out of credits"}`, domain.ClassAuth},
		{"millionverifier-credits-quota", adapters.MillionVerifier, "millionverifier:default",
			`{"result":"","error":"Insufficient credits"}`, domain.ClassQuota},
		{"millionverifier-badkey-auth", adapters.MillionVerifier, "millionverifier:default",
			`{"result":"","error":"Invalid API key"}`, domain.ClassAuth},
		{"debounce-wrongapi-auth", adapters.DeBounce, "debounce:default",
			`{"success":"0","debounce":{"error":"Wrong API","code":"0"}}`, domain.ClassAuth},
		{"clearout-failure-quota", adapters.Clearout, "clearout:default",
			`{"status":"failure","error":{"code":1009,"message":"Not enough credits available"}}`, domain.ClassQuota},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			a := tc.newA(srv.URL, clientWith(srv, tc.pool, "BADKEY"))
			_, err := a.Fetch(context.Background(), emailReq())
			if got := domain.ClassOf(err); got != tc.want {
				t.Fatalf("%s: 200-with-error should map to %s, got %s (%v)", tc.name, tc.want, got, err)
			}
		})
	}
}

// emailReq is a standard verification input carrying a work_email to check.
func emailReq() provider.Request {
	return provider.Request{Known: map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com"}}
}

// Wave-0 adapters (the "Recommended Starting Stack") are exercised here by a table-driven
// fixture-decode + egress-injection test: each pinned (UNVERIFIED) fixture is served through the
// real adapter + AuthInjector, and the decoded canonical Fields are asserted. A fixture that
// drifts from Decode, or a mapping regression, fails the build. The shared HTTP-status->error-class
// mapping is proven once in TestAdapters_StatusErrorMatrix (live_smoke_test.go); per-provider
// status quirks get their own assertion below.
func TestWave0_DecodeFixtures(t *testing.T) {
	cases := []struct {
		name    string
		newA    func(string, *http.Client) *provider.HTTPAdapter
		pool    string
		fixture string
		req     provider.Request
		// want maps a canonical Field to its expected decoded value; every listed Field must be
		// present with confidence > 0.
		want map[domain.Field]string
	}{
		{
			name:    "people-data-labs",
			newA:    adapters.PeopleDataLabs,
			pool:    "people-data-labs:default",
			fixture: "testdata/people-data-labs_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldJobTitle:      "vp sales",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldIndustry:      "software",
				domain.FieldEmployeeCount: "1001-5000",
			},
		},
		{
			name:    "neverbounce",
			newA:    adapters.NeverBounce,
			pool:    "neverbounce:default",
			fixture: "testdata/neverbounce_found.json",
			req:     emailReq(),
			want:    map[domain.Field]string{domain.FieldEmailStatus: "valid"},
		},
		{
			name:    "kickbox",
			newA:    adapters.Kickbox,
			pool:    "kickbox:default",
			fixture: "testdata/kickbox_found.json",
			req:     emailReq(),
			want:    map[domain.Field]string{domain.FieldEmailStatus: "deliverable"},
		},
		{
			name:    "zerobounce",
			newA:    adapters.ZeroBounce,
			pool:    "zerobounce:default",
			fixture: "testdata/zerobounce_found.json",
			req:     emailReq(),
			want: map[domain.Field]string{
				domain.FieldEmailStatus: "valid",
				domain.FieldFirstName:   "jane",
				domain.FieldLastName:    "doe",
			},
		},
		{
			name: "emailable", newA: adapters.Emailable, pool: "emailable:default",
			fixture: "testdata/emailable_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "deliverable", domain.FieldFullName: "Jane Doe"},
		},
		{
			name: "bouncer", newA: adapters.Bouncer, pool: "bouncer:default",
			fixture: "testdata/bouncer_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "deliverable"},
		},
		{
			name: "mailgun-validate", newA: adapters.MailgunValidate, pool: "mailgun-validate:default",
			fixture: "testdata/mailgun-validate_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "deliverable"},
		},
		{
			name: "millionverifier", newA: adapters.MillionVerifier, pool: "millionverifier:default",
			fixture: "testdata/millionverifier_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "ok"},
		},
		{
			name: "debounce", newA: adapters.DeBounce, pool: "debounce:default",
			fixture: "testdata/debounce_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "Safe to Send"},
		},
		{
			name: "clearout", newA: adapters.Clearout, pool: "clearout:default",
			fixture: "testdata/clearout_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid"},
		},
		{
			name: "datagma", newA: adapters.Datagma, pool: "datagma:default",
			fixture: "testdata/datagma_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "valid",
				domain.FieldCompanyDomain: "acme.com",
			},
		},
		// L5 phone-validation — all normalize to a phone_status string; person() carries mobile_phone.
		{
			name: "telnyx", newA: adapters.Telnyx, pool: "telnyx:default",
			fixture: "testdata/telnyx_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "vonage", newA: adapters.Vonage, pool: "vonage:default",
			fixture: "testdata/vonage_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile"},
		},
		{
			name: "messagebird", newA: adapters.MessageBird, pool: "messagebird:default",
			fixture: "testdata/messagebird_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "ipqualityscore", newA: adapters.IPQualityScore, pool: "ipqualityscore:default",
			fixture: "testdata/ipqualityscore_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "plivo", newA: adapters.Plivo, pool: "plivo:default",
			fixture: "testdata/plivo_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "infobip", newA: adapters.Infobip, pool: "infobip:default",
			fixture: "testdata/infobip_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile"},
		},
		{
			name: "numverify", newA: adapters.NumVerify, pool: "numverify:default",
			fixture: "testdata/numverify_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "abstract-phone", newA: adapters.AbstractPhone, pool: "abstract-phone:default",
			fixture: "testdata/abstract-phone_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "veriphone", newA: adapters.Veriphone, pool: "veriphone:default",
			fixture: "testdata/veriphone_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "byteplant-phone", newA: adapters.Byteplant, pool: "byteplant-phone:default",
			fixture: "testdata/byteplant-phone_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+1 555-555-0100"},
		},
		{
			name: "telesign", newA: adapters.Telesign, pool: "telesign:default",
			fixture: "testdata/telesign_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile"},
		},
		{
			name: "lusha", newA: adapters.Lusha, pool: "lusha:default",
			fixture: "testdata/lusha_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldDirectDial:    "+15555550111",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldFullName:      "Jane Doe",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
			},
		},
		{
			name: "kaspr", newA: adapters.Kaspr, pool: "kaspr:default",
			fixture: "testdata/kaspr_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldFirstName:     "Jane",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
			},
		},
		{
			name: "contactout", newA: adapters.ContactOut, pool: "contactout:default",
			fixture: "testdata/contactout_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
				domain.FieldEmailStatus:   "Verified",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
			},
		},
		{
			name: "diffbot", newA: adapters.Diffbot, pool: "diffbot:default",
			fixture: "testdata/diffbot_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldEmployeeCount:      "3200",
				domain.FieldCompanyRevenue:     "62753000000",
				domain.FieldCompanyFoundedYear: "2012",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
				domain.FieldNAICS:              "511210",
				domain.FieldSIC:                "7372",
			},
		},
		{
			name: "hg-insights", newA: adapters.HGInsights, pool: "hg-insights:default",
			fixture: "testdata/hg-insights_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldTechnographics:   "AWS,Salesforce",
				domain.FieldCompanyName:      "Acme",
				domain.FieldCompanyDomain:    "acme.com",
				domain.FieldCompanyHQCountry: "US",
				domain.FieldEmployeeCount:    "3200",
				domain.FieldCompanyRevenue:   "680985000",
			},
		},
		{
			name: "vainu", newA: adapters.Vainu, pool: "vainu:default",
			fixture: "testdata/vainu_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme Oy",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldCompanyHQCountry:   "FI",
				domain.FieldEmployeeCount:      "39",
				domain.FieldCompanyRevenue:     "895766",
				domain.FieldIndustry:           "62010",
				domain.FieldCompanyType:        "Limited company",
				domain.FieldCompanyFoundedYear: "2013",
				domain.FieldTechnographics:     "AWS,HubSpot",
			},
		},
		{
			name: "global-database", newA: adapters.GlobalDatabase, pool: "global-database:default",
			fixture: "testdata/global-database_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme PLC",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldCompanyPhone:       "+441992632222",
				domain.FieldEmployeeCount:      "10001",
				domain.FieldCompanyFoundedYear: "1947",
				domain.FieldIndustry:           "Retail",
				domain.FieldSIC:                "47110",
				domain.FieldCompanyHQCountry:   "United Kingdom",
				domain.FieldCompanyType:        "Public Limited Company",
				domain.FieldCompanyLinkedInURL: "https://linkedin.com/company/-acme",
			},
		},
		{
			name: "data-axle", newA: adapters.DataAxle, pool: "data-axle:default",
			fixture: "testdata/data-axle_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme Inc",
				domain.FieldCompanyPhone:       "+15555550100",
				domain.FieldCompanyHQCity:      "Hilton Head Island",
				domain.FieldCompanyHQCountry:   "US",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
				domain.FieldCompanyType:        "headquarters",
				domain.FieldCompanyFoundedYear: "2001",
			},
		},
		{
			name: "owler", newA: adapters.Owler, pool: "owler:default",
			fixture: "testdata/owler_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldCompanyType:        "Public",
				domain.FieldEmployeeCount:      "10000+",
				domain.FieldCompanyRevenue:     "$50B+",
				domain.FieldCompanyFoundedYear: "1970",
				domain.FieldIndustry:           "Aerospace & Defense",
				domain.FieldCompanyHQCountry:   "Netherlands",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
			},
		},
		{
			name: "leadspace", newA: adapters.Leadspace, pool: "leadspace:default",
			fixture: "testdata/leadspace_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:      "Acme",
				domain.FieldCompanyDomain:    "acme.com",
				domain.FieldIndustry:         "Software",
				domain.FieldCompanyType:      "Private",
				domain.FieldEmployeeCount:    "1200",
				domain.FieldCompanyRevenue:   "250000000",
				domain.FieldCompanyHQCountry: "United States",
				domain.FieldTechnographics:   "AWS,Salesforce",
			},
		},
		{
			name: "ninjapear", newA: adapters.NinjaPear, pool: "ninjapear:default",
			fixture: "testdata/ninjapear_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldIndustry:           "45102010",
				domain.FieldCompanyType:        "PRIVATELY_HELD",
				domain.FieldCompanyFoundedYear: "2010",
				domain.FieldEmployeeCount:      "8000",
				domain.FieldCompanyHQCountry:   "United States",
				domain.FieldCompanyHQCity:      "South San Francisco",
			},
		},
		{
			name: "uplead", newA: adapters.UpLead, pool: "uplead:default",
			fixture: "testdata/uplead_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "Valid",
				domain.FieldMobilePhone:   "(415) 426-8562",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldSeniority:     "VP",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldIndustry:      "Software",
				domain.FieldEmployeeCount: "3200",
				domain.FieldFirstName:     "Jane",
			},
		},
		{
			name: "adapt-io", newA: adapters.AdaptIO, pool: "adapt-io:default",
			fixture: "testdata/adapt-io_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldDirectDial:    "+15555550111",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldSeniority:     "VP",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldEmployeeCount: "3200",
				domain.FieldIndustry:      "Software",
			},
		},
		{
			name: "aeroleads", newA: adapters.AeroLeads, pool: "aeroleads:default",
			fixture: "testdata/aeroleads_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "1.0",
				domain.FieldFullName:      "Jane Doe",
				domain.FieldCompanyDomain: "acme.com",
			},
		},
		{
			name: "scrubby", newA: adapters.Scrubby, pool: "scrubby:default",
			fixture: "testdata/scrubby_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid"},
		},
		{
			name: "enrichley", newA: adapters.Enrichley, pool: "enrichley:default",
			fixture: "testdata/enrichley_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "catch_all_validated"},
		},
		{
			name: "mailfloss", newA: adapters.Mailfloss, pool: "mailfloss:default",
			fixture: "testdata/mailfloss_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "passed"},
		},
		// Wave 9 — additional email verifiers + phone validators (net-new, beyond the 200-tool sheet).
		{
			name: "quickemailverification", newA: adapters.QuickEmailVerification, pool: "quickemailverification:default",
			fixture: "testdata/quickemailverification_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid", domain.FieldCompanyDomain: "acme.com"},
		},
		{
			name: "myemailverifier", newA: adapters.MyEmailVerifier, pool: "myemailverifier:default",
			fixture: "testdata/myemailverifier_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "Valid", domain.FieldWorkEmail: "jane@acme.com"},
		},
		{
			name: "mailboxvalidator", newA: adapters.MailboxValidator, pool: "mailboxvalidator:default",
			fixture: "testdata/mailboxvalidator_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid", domain.FieldWorkEmail: "jane@acme.com", domain.FieldCompanyDomain: "acme.com"},
		},
		{
			name: "bouncify", newA: adapters.Bouncify, pool: "bouncify:default",
			fixture: "testdata/bouncify_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "deliverable", domain.FieldWorkEmail: "jane@acme.com"},
		},
		{
			name: "emaillistverify", newA: adapters.EmailListVerify, pool: "emaillistverify:default",
			fixture: "testdata/emaillistverify_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "ok", domain.FieldFirstName: "Jane", domain.FieldLastName: "Doe"},
		},
		{
			name: "trestle", newA: adapters.Trestle, pool: "trestle:default",
			fixture: "testdata/trestle_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile"},
		},
		{
			name: "numlookupapi", newA: adapters.NumLookupAPI, pool: "numlookupapi:default",
			fixture: "testdata/numlookupapi_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+14158586273"},
		},
		{
			name: "companyenrich", newA: adapters.CompanyEnrich, pool: "companyenrich:default",
			fixture: "testdata/companyenrich_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Wise",
				domain.FieldCompanyDomain:      "wise.com",
				domain.FieldFundingStage:       "post_ipo_debt",
				domain.FieldCompanyFoundedYear: "2011",
				domain.FieldCompanyHQCountry:   "Estonia",
				domain.FieldNAICS:              "522110,522210,522298,522320",
			},
		},
		{
			name: "enrich-so", newA: adapters.EnrichSo, pool: "enrich-so:default",
			fixture: "testdata/enrich-so_found.json", req: emailReq(),
			want: map[domain.Field]string{
				domain.FieldFullName:    "Emily Zhang",
				domain.FieldFirstName:   "Emily",
				domain.FieldLastName:    "Zhang",
				domain.FieldLinkedInURL: "https://www.linkedin.com/in/emilyzhang",
				domain.FieldCompanyName: "Figma",
				domain.FieldJobTitle:    "Staff Product Designer",
			},
		},
		{
			name: "voila-norbert", newA: adapters.VoilaNorbert, pool: "voila-norbert:default",
			fixture: "testdata/voila-norbert_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldEmailStatus: "95"},
		},
		// Wave 10 — further net-new providers.
		{
			name: "neutrinoapi", newA: adapters.NeutrinoAPI, pool: "neutrinoapi:default",
			fixture: "testdata/neutrinoapi_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+15555550100"},
		},
		{
			name: "cloudmersive", newA: adapters.Cloudmersive, pool: "cloudmersive:default",
			fixture: "testdata/cloudmersive_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid", domain.FieldCompanyDomain: "acme.com"},
		},
		{
			name: "abstract-email", newA: adapters.AbstractEmail, pool: "abstract-email:default",
			fixture: "testdata/abstract-email_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "DELIVERABLE"},
		},
		{
			name: "mailercheck", newA: adapters.MailerCheck, pool: "mailercheck:default",
			fixture: "testdata/mailercheck_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid"},
		},
		{
			name: "reoon", newA: adapters.Reoon, pool: "reoon:default",
			fixture: "testdata/reoon_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "safe", domain.FieldWorkEmail: "jane@acme.com"},
		},
		{
			name: "mails-so", newA: adapters.MailsSo, pool: "mails-so:default",
			fixture: "testdata/mails-so_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "deliverable", domain.FieldWorkEmail: "jane@acme.com"},
		},
		{
			name: "emailhippo", newA: adapters.EmailHippo, pool: "emailhippo:default",
			fixture: "testdata/emailhippo_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "Ok", domain.FieldCompanyDomain: "acme.com"},
		},
		{
			name: "truelist", newA: adapters.Truelist, pool: "truelist:default",
			fixture: "testdata/truelist_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "ok", domain.FieldWorkEmail: "jane@acme.com", domain.FieldCompanyDomain: "acme.com"},
		},
		{
			name: "bigpicture", newA: adapters.BigPicture, pool: "bigpicture:default",
			fixture: "testdata/bigpicture_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldNAICS:              "541511",
				domain.FieldCompanyFoundedYear: "2010",
				domain.FieldEmployeeCount:      "3200",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
			},
		},
		{
			name: "enformion", newA: adapters.Enformion, pool: "enformion:default",
			fixture: "testdata/enformion_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldFirstName:     "Jane",
				domain.FieldLastName:      "Doe",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldPhoneStatus:   "valid_mobile",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
			},
		},
		// Wave 11 — official open-data registries (AuthNone).
		{
			name: "gleif", newA: adapters.GLEIF, pool: "gleif:default",
			fixture: "testdata/gleif_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Bloomberg Finance L.P.",
				domain.FieldCompanyHQCountry:   "US",
				domain.FieldCompanyHQCity:      "New York",
				domain.FieldCompanyType:        "T91T",
				domain.FieldCompanyFoundedYear: "2007",
			},
		},
		{
			name: "recherche-entreprises", newA: adapters.RechercheEntreprises, pool: "recherche-entreprises:default",
			fixture: "testdata/recherche-entreprises_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "DIRECTION INTERMINISTERIELLE DU NUMERIQUE (DINUM)",
				domain.FieldIndustry:           "84.11Z",
				domain.FieldCompanyType:        "7120",
				domain.FieldCompanyFoundedYear: "2017",
				domain.FieldCompanyHQCity:      "PARIS",
				domain.FieldCompanyHQCountry:   "FR",
				domain.FieldCompanyRevenue:     "12000000",
			},
		},
		{
			name: "north-data", newA: adapters.NorthData, pool: "north-data:default",
			fixture: "testdata/north-data_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:      "1000mikes AG",
				domain.FieldCompanyType:      "AG",
				domain.FieldCompanyHQCity:    "Hamburg",
				domain.FieldCompanyHQCountry: "DE",
				domain.FieldIndustry:         "01.13",
				domain.FieldNAICS:            "111991",
				domain.FieldCompanyRevenue:   "5400000",
				domain.FieldEmployeeCount:    "42",
				domain.FieldCompanyDomain:    "1000mikes.com",
				domain.FieldCompanyPhone:     "+49 40 555 0100",
			},
		},
		{
			name: "opensanctions", newA: adapters.OpenSanctions, pool: "opensanctions:default",
			fixture: "testdata/opensanctions_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:      "ACME HOLDINGS LLC",
				domain.FieldCompanyHQCountry: "RU",
			},
		},
		{
			name: "verimail", newA: adapters.Verimail, pool: "verimail:default",
			fixture: "testdata/verimail_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "deliverable", domain.FieldWorkEmail: "jane@acme.com"},
		},
		{
			name: "mailboxlayer", newA: adapters.Mailboxlayer, pool: "mailboxlayer:default",
			fixture: "testdata/mailboxlayer_found.json", req: emailReq(),
			want: map[domain.Field]string{
				domain.FieldEmailStatus:   "valid",
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldCompanyDomain: "acme.com",
			},
		},
		{
			name: "melissa-global-phone", newA: adapters.MelissaGlobalPhone, pool: "melissa-global-phone:default",
			fixture: "testdata/melissa-global-phone_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldPhoneStatus: "valid_unknown",
				domain.FieldMobilePhone: "+15555550100",
			},
		},
		{
			name: "loqate-phone", newA: adapters.LoqatePhone, pool: "loqate-phone:default",
			fixture: "testdata/loqate-phone_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldPhoneStatus: "valid_mobile",
				domain.FieldMobilePhone: "+15555550100",
			},
		},
		// Wave 12 — official registries (Czech ARES no-auth, Ireland CRO Basic).
		{
			name: "ares-cz", newA: adapters.AresCZ, pool: "ares-cz:default",
			fixture: "testdata/ares-cz_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "První certifikační autorita, a.s.",
				domain.FieldCompanyHQCountry:   "Česká republika",
				domain.FieldCompanyHQCity:      "Praha",
				domain.FieldCompanyType:        "121",
				domain.FieldCompanyFoundedYear: "2001",
				domain.FieldIndustry:           "47780,62",
			},
		},
		{
			name: "cro-ie", newA: adapters.CroIE, pool: "cro-ie:default",
			fixture: "testdata/cro-ie_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "GOOGLE IRELAND LIMITED",
				domain.FieldCompanyType:        "Private Company Limited by Shares",
				domain.FieldCompanyFoundedYear: "2003",
			},
		},
		{
			name: "sendgrid-validation", newA: adapters.SendGridValidation, pool: "sendgrid-validation:default",
			fixture: "testdata/sendgrid-validation_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "Valid"},
		},
		{
			name: "proofy", newA: adapters.Proofy, pool: "proofy:default",
			fixture: "testdata/proofy_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid"},
		},
		{
			name: "captainverify", newA: adapters.CaptainVerify, pool: "captainverify:default",
			fixture: "testdata/captainverify_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "valid"},
		},
		{
			name: "charity-commission-uk", newA: adapters.CharityCommissionUK, pool: "charity-commission-uk:default",
			fixture: "testdata/charity-commission-uk_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "CANCER RESEARCH UK",
				domain.FieldCompanyType:        "charity",
				domain.FieldCompanyFoundedYear: "2005",
			},
		},
		{
			name: "data8-phone", newA: adapters.Data8Phone, pool: "data8-phone:default",
			fixture: "testdata/data8-phone_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile", domain.FieldMobilePhone: "+447700900123"},
		},
		{
			name: "nymblr", newA: adapters.Nymblr, pool: "nymblr:default",
			fixture: "testdata/nymblr_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldDirectDial:    "+14155550111",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldCompanyName:   "Acme",
				domain.FieldEmployeeCount: "3200",
				domain.FieldNAICS:         "541511",
				domain.FieldSIC:           "7372",
			},
		},
		{
			name: "kendo", newA: adapters.Kendo, pool: "kendo:default",
			fixture: "testdata/kendo_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldWorkEmail: "jane@acme.com", domain.FieldPersonalEmail: "jane.doe@gmail.com"},
		},
		{
			name: "extruct", newA: adapters.Extruct, pool: "extruct:default",
			fixture: "testdata/extruct_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldEmployeeCount: "3200",
			},
		},
		{
			name: "reverse-contact", newA: adapters.ReverseContact, pool: "reverse-contact:default",
			fixture: "testdata/reverse-contact_found.json", req: emailReq(),
			want: map[domain.Field]string{
				domain.FieldFirstName:   "Jane",
				domain.FieldLastName:    "Doe",
				domain.FieldFullName:    "Jane Doe",
				domain.FieldJobTitle:    "VP Sales",
				domain.FieldLinkedInURL: "https://www.linkedin.com/in/janedoe",
				domain.FieldCompanyName: "Acme",
			},
		},
		{
			name: "leadmagic", newA: adapters.LeadMagic, pool: "leadmagic:default",
			fixture: "testdata/leadmagic_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "valid",
				domain.FieldCompanyName:   "Acme",
				domain.FieldEmployeeCount: "51-200",
				domain.FieldIndustry:      "Software",
			},
		},
		{
			name: "getprospect", newA: adapters.GetProspect, pool: "getprospect:default",
			fixture: "testdata/getprospect_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "valid",
				domain.FieldCompanyDomain: "acme.com",
			},
		},
		{
			name: "skrapp", newA: adapters.Skrapp, pool: "skrapp:default",
			fixture: "testdata/skrapp_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "valid",
				domain.FieldFirstName:     "Jane",
				domain.FieldFullName:      "Jane Doe",
				domain.FieldCompanyDomain: "acme.com",
			},
		},
		{
			name: "tomba", newA: adapters.Tomba, pool: "tomba:default",
			fixture: "testdata/tomba_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "valid",
				domain.FieldFirstName:     "Jane",
				domain.FieldLastName:      "Doe",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
			},
		},
		{
			name: "cufinder", newA: adapters.Cufinder, pool: "cufinder:default",
			fixture: "testdata/cufinder_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldFullName:      "Jane Doe",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldIndustry:      "Software",
				domain.FieldEmployeeCount: "1,001-5,000",
			},
		},
		{
			name: "bounceban", newA: adapters.BounceBan, pool: "bounceban:default",
			fixture: "testdata/bounceban_found.json", req: emailReq(),
			want: map[domain.Field]string{domain.FieldEmailStatus: "deliverable"},
		},
		{
			name: "realphonevalidation", newA: adapters.RealPhoneValidation, pool: "realphonevalidation:default",
			fixture: "testdata/realphonevalidation_found.json", req: person(),
			want: map[domain.Field]string{domain.FieldPhoneStatus: "valid_mobile"},
		},
		{
			name: "abstract-company", newA: adapters.AbstractCompany, pool: "abstract-company:default",
			fixture: "testdata/abstract-company_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldEmployeeCount:      "3200",
				domain.FieldIndustry:           "Software",
				domain.FieldCompanyFoundedYear: "2012",
				domain.FieldCompanyHQCountry:   "United States",
				domain.FieldCompanyType:        "public",
				domain.FieldCompanyLinkedInURL: "https://linkedin.com/company/acme",
			},
		},
		{
			name: "cleanlist", newA: adapters.Cleanlist, pool: "cleanlist:default",
			fixture: "testdata/cleanlist_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldIndustry:           "Software",
				domain.FieldCompanyRevenue:     "$10M-$50M",
				domain.FieldEmployeeCount:      "1200",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
			},
		},
		{
			name: "predictleads", newA: adapters.PredictLeads, pool: "predictleads:default",
			fixture: "testdata/predictleads_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:      "Acme",
				domain.FieldCompanyDomain:    "acme.com",
				domain.FieldCompanyHQCountry: "United States",
			},
		},
		{
			name: "signalhire", newA: adapters.SignalHire, pool: "signalhire:default",
			fixture: "testdata/signalhire_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldFullName:      "Jane Doe",
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
			},
		},
		{
			name: "mixrank", newA: adapters.MixRank, pool: "mixrank:default",
			fixture: "testdata/mixrank_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme Inc",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldCompanyType:        "Privately Held",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
				domain.FieldCompanyHQCountry:   "US",
				domain.FieldCompanyHQCity:      "San Francisco",
				domain.FieldEmployeeCount:      "540",
				domain.FieldCompanyFoundedYear: "2008",
				domain.FieldIndustry:           "Internet",
				domain.FieldSIC:                "7372",
				domain.FieldNAICS:              "511210",
			},
		},
		{
			name: "pipl", newA: adapters.Pipl, pool: "pipl:default",
			fixture: "testdata/pipl_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldFullName:      "Jane Doe",
				domain.FieldFirstName:     "Jane",
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
				domain.FieldMobilePhone:   "+1 555-555-0100",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldCompanyName:   "Acme",
			},
		},
		{
			name: "versium", newA: adapters.Versium, pool: "versium:default",
			fixture: "testdata/versium_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldFirstName:     "Jane",
				domain.FieldLastName:      "Doe",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
				domain.FieldMobilePhone:   "+15555550100",
			},
		},
		{
			name: "salesintel", newA: adapters.SalesIntel, pool: "salesintel:default",
			fixture: "testdata/salesintel_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldPersonalEmail: "jane.doe@gmail.com",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldDirectDial:    "+15555550111",
				domain.FieldOfficePhone:   "+15555550199",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldFullName:      "Jane Doe",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldLinkedInURL:   "https://www.linkedin.com/in/janedoe",
				domain.FieldNAICS:         "511210",
			},
		},
		{
			name:    "findymail",
			newA:    adapters.Findymail,
			pool:    "findymail:default",
			fixture: "testdata/findymail_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldFullName:      "Jane Doe",
				domain.FieldCompanyDomain: "acme.com",
			},
		},
		{
			name:    "anymailfinder",
			newA:    adapters.AnymailFinder,
			pool:    "anymailfinder:default",
			fixture: "testdata/anymailfinder_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:   "jane@acme.com",
				domain.FieldEmailStatus: "valid",
			},
		},
		{
			name:    "apollo",
			newA:    adapters.Apollo,
			pool:    "apollo:default",
			fixture: "testdata/apollo_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldEmailStatus:   "verified",
				domain.FieldJobTitle:      "VP Sales",
				domain.FieldFullName:      "Jane Doe",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
			},
		},
		{
			name:    "clearbit",
			newA:    adapters.Clearbit,
			pool:    "clearbit:default",
			fixture: "testdata/clearbit_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldIndustry:           "Internet Software & Services",
				domain.FieldEmployeeCount:      "60",
				domain.FieldCompanyRevenue:     "$10M-$50M",
				domain.FieldTechnographics:     "aws_route_53,mongodb,nginx",
				domain.FieldCompanyHQCountry:   "United States",
				domain.FieldCompanyFoundedYear: "2012",
				domain.FieldCompanyType:        "private",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme-corp",
			},
		},
		{
			name:    "builtwith",
			newA:    adapters.BuiltWith,
			pool:    "builtwith:default",
			fixture: "testdata/builtwith_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldTechnographics:   "Cloudflare,React,nginx",
				domain.FieldCompanyName:      "Acme",
				domain.FieldIndustry:         "Technology and Computing",
				domain.FieldCompanyHQCountry: "US",
				domain.FieldEmployeeCount:    "100",
			},
		},
		{
			name:    "theirstack",
			newA:    adapters.TheirStack,
			pool:    "theirstack:default",
			fixture: "testdata/theirstack_found.json",
			req:     person(),
			want:    map[domain.Field]string{domain.FieldTechnographics: "Elasticsearch,Kafka"},
		},
		{
			name:    "wappalyzer",
			newA:    adapters.Wappalyzer,
			pool:    "wappalyzer:default",
			fixture: "testdata/wappalyzer_found.json",
			req:     person(),
			want:    map[domain.Field]string{domain.FieldTechnographics: "Nginx,React"},
		},
		{
			name:    "brandfetch",
			newA:    adapters.Brandfetch,
			pool:    "brandfetch:default",
			fixture: "testdata/brandfetch_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldEmployeeCount:      "500",
				domain.FieldCompanyFoundedYear: "2012",
				domain.FieldIndustry:           "Software",
				domain.FieldCompanyType:        "PRIVATELY_HELD",
				domain.FieldCompanyHQCountry:   "United States",
				domain.FieldCompanyHQCity:      "Frisco",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
			},
		},
		{
			name: "crunchbase", newA: adapters.Crunchbase, pool: "crunchbase:default",
			fixture: "testdata/crunchbase_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldCompanyFoundedYear: "2012",
				domain.FieldCompanyType:        "for_profit",
				domain.FieldFundingStage:       "series_a",
				domain.FieldIndustry:           "SaaS,Software",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
			},
		},
		{
			name: "opencorporates", newA: adapters.OpenCorporates, pool: "opencorporates:default",
			fixture: "testdata/opencorporates_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "ACME LTD",
				domain.FieldCompanyFoundedYear: "2010",
				domain.FieldCompanyHQCountry:   "gb",
				domain.FieldCompanyType:        "Private Limited Company",
				domain.FieldCompanyHQCity:      "London",
			},
		},
		{
			name: "ocean-io", newA: adapters.OceanIO, pool: "ocean-io:default",
			fixture: "testdata/ocean-io_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldCompanyName:        "Acme",
				domain.FieldEmployeeCount:      "51-200",
				domain.FieldCompanyHQCountry:   "US",
				domain.FieldCompanyRevenue:     "10-50M",
				domain.FieldCompanyFoundedYear: "2012",
				domain.FieldIndustry:           "B2B,SaaS",
				domain.FieldTechnographics:     "HubSpot,Salesforce",
				domain.FieldFundingStage:       "Series A",
			},
		},
		{
			name: "the-companies-api", newA: adapters.TheCompaniesAPI, pool: "the-companies-api:default",
			fixture: "testdata/the-companies-api_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldIndustry:           "internet",
				domain.FieldEmployeeCount:      "300",
				domain.FieldCompanyRevenue:     "10m-50m",
				domain.FieldCompanyFoundedYear: "2012",
				domain.FieldCompanyType:        "privately-held",
				domain.FieldCompanyHQCity:      "Frisco",
				domain.FieldNAICS:              "5182",
				domain.FieldSIC:                "7372",
				domain.FieldTechnographics:     "nginx,react",
			},
		},
		{
			name: "coresignal", newA: adapters.Coresignal, pool: "coresignal:default",
			fixture: "testdata/coresignal_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldIndustry:           "Software Development",
				domain.FieldEmployeeCount:      "548",
				domain.FieldCompanyFoundedYear: "2015",
				domain.FieldCompanyHQCountry:   "United States",
				domain.FieldNAICS:              "541511,541512",
				domain.FieldFundingStage:       "Series A",
			},
		},
		{
			name: "fullcontact", newA: adapters.FullContact, pool: "fullcontact:default",
			fixture: "testdata/fullcontact_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyName:        "Acme, Inc.",
				domain.FieldCompanyDomain:      "acme.com",
				domain.FieldCompanyLinkedInURL: "https://www.linkedin.com/company/acme",
				domain.FieldEmployeeCount:      "350",
				domain.FieldCompanyFoundedYear: "2010",
				domain.FieldIndustry:           "Software",
				domain.FieldCompanyHQCity:      "Denver",
				domain.FieldCompanyHQCountry:   "United States",
				domain.FieldSIC:                "737",
				domain.FieldNAICS:              "5182",
			},
		},
		{
			name: "storeleads", newA: adapters.Storeleads, pool: "storeleads:default",
			fixture: "testdata/storeleads_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldCompanyDomain:    "acme.com",
				domain.FieldCompanyName:      "Acme",
				domain.FieldCompanyHQCountry: "US",
				domain.FieldEmployeeCount:    "1390",
				domain.FieldCompanyRevenue:   "317891239",
				domain.FieldTechnographics:   "Cloudflare,Klaviyo,shopify",
				domain.FieldIndustry:         "Apparel & Accessories",
			},
		},
		{
			name:    "g2",
			newA:    adapters.G2,
			pool:    "g2:default",
			fixture: "testdata/g2_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldBuyingSignal:     "pricing",
				domain.FieldIntentTopics:     "CRM",
				domain.FieldCompanyName:      "Enterprise Corp",
				domain.FieldCompanyDomain:    "enterprise.com",
				domain.FieldIndustry:         "Software",
				domain.FieldCompanyHQCountry: "US",
				domain.FieldEmployeeCount:    "500",
			},
		},
		{
			name: "6sense", newA: adapters.SixSense, pool: "6sense:default",
			fixture: "testdata/6sense_found.json", req: person(),
			want: map[domain.Field]string{
				domain.FieldIntentScore:    "82",
				domain.FieldBuyingSignal:   "Decision",
				domain.FieldIntentTopics:   "Cloud Migration Intent,Enterprise Target Accounts",
				domain.FieldCompanyDomain:  "acme.com",
				domain.FieldCompanyName:    "Acme Corporation",
				domain.FieldIndustry:       "Software",
				domain.FieldEmployeeCount:  "3200",
				domain.FieldCompanyRevenue: "750000000",
				domain.FieldNAICS:          "511210",
				domain.FieldSIC:            "7372",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveFixture(t, tc.fixture)
			defer srv.Close()
			a := tc.newA(srv.URL, clientWith(srv, tc.pool, "SECRET"))
			res, err := a.Fetch(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("%s fetch: %v", tc.name, err)
			}
			for f, wantVal := range tc.want {
				got, ok := res.Values[f]
				if !ok {
					t.Errorf("%s: missing %s", tc.name, f)
					continue
				}
				if got.Value != wantVal {
					t.Errorf("%s: %s = %q, want %q", tc.name, f, got.Value, wantVal)
				}
				if got.Confidence <= 0 || got.Confidence > 1 {
					t.Errorf("%s: %s confidence %v out of (0,1]", tc.name, f, got.Confidence)
				}
			}
		})
	}
}
