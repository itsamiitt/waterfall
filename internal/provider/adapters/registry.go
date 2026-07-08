package adapters

import (
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/provider"
)

// This file is the adapter REGISTRY — the single source of truth binding each provider slug
// to its constructor plus the catalog metadata the seeder needs. It is the bridge between the
// two halves of the system (ADR-0023):
//
//   - the in-process engine wires the adapters via All(egressClient);
//   - the dashboard catalog is seeded by introspecting each constructed *provider.HTTPAdapter
//     (its NameV / BaseURL / Auth / Caps) and combining it with the Registered metadata.
//
// Because both paths read this one list, a runtime adapter and its `providers` catalog row can
// never drift. Adding a provider = append ONE Registered entry here + its `<slug>.go` file. No
// init() magic, no codegen — an explicit slice, matching the project's "explicit over dynamic"
// style (engine.New / router.New already take an explicit []Adapter).

// Registered is one entry in the adapter registry.
type Registered struct {
	// Slug is the stable provider id. It MUST equal the constructed adapter's NameV
	// (asserted by TestRegistry_SlugMatchesAdapterName) and is used as the catalog row id
	// and the "<slug>:default" key-pool selector prefix.
	Slug string
	// New constructs a synchronous adapter. base=="" selects the production default BaseURL;
	// tests pass an httptest URL. The shared egress client carries key injection + SSRF guard.
	// Exactly one of New / NewAsync is set per entry.
	New func(base string, c *http.Client) *provider.HTTPAdapter
	// NewAsync constructs a multi-round-trip adapter (submit→poll / match→fetch, ADR-0024 Phase 3)
	// for providers that cannot be expressed as a single request/response. Same base/client
	// contract as New. Set INSTEAD of New for async providers (e.g. D&B, Verifalia batch).
	NewAsync func(base string, c *http.Client) *provider.AsyncHTTPAdapter
	// Category is the spreadsheet pipeline layer, e.g. "identity", "email-find",
	// "email-verify", "phone-find", "phone-validate", "firmographics", "technographics",
	// "intent", "orchestration". Free text (providers.category has no CHECK constraint).
	Category string
	// Status is the ADR-0009 inclusion verdict: "ACTIVE-CANDIDATE" (clean API-first) or
	// "DEPRIORITIZED" (licensed API but public-web/LinkedIn provenance; off by default until a
	// per-provider compliance review). EXCLUDED providers are NOT registered (see docs/03 §6).
	Status string
	// Regions is the coverage hint, e.g. ["global"], ["EU"], ["US"]; seeded into providers.region.
	Regions []string
	// DocsURL is the vendor's API documentation root (provenance for the researched shape).
	DocsURL string
}

// registry is append-only, ordered by pipeline layer then slug.
var registry = []Registered{
	// L1 — Source / identity + firmographics.
	{Slug: "people-data-labs", New: PeopleDataLabs, Category: "identity", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.peopledatalabs.com/docs/reference-person-enrichment-api"},
	{Slug: "coresignal", New: Coresignal, Category: "identity", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.coresignal.com/company-api/multi-source-company-api/enrich"},
	{Slug: "enrich-so", New: EnrichSo, Category: "identity", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://doc.enrich.so/look-up-a-professional-profile-by-email-27483203e0"},
	{Slug: "enformion", New: Enformion, Category: "identity", Status: "DEPRIORITIZED", Regions: []string{"US"}, DocsURL: "https://enformiongo.readme.io/reference/contact-enrichment"},
	{Slug: "fullcontact", New: FullContact, Category: "identity", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.fullcontact.com/docs/company-enrich-overview"},

	// L2 — Email finding.
	{Slug: "hunter", New: Hunter, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://hunter.io/api-documentation/v2"},
	{Slug: "prospeo", New: Prospeo, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.prospeo.io/"},
	{Slug: "apollo", New: Apollo, Category: "email-find", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.apollo.io/reference/people-enrichment"},
	{Slug: "lusha", New: Lusha, Category: "email-find", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.lusha.com/apis/openapi/contact-search-and-enrich"},
	{Slug: "kaspr", New: Kaspr, Category: "email-find", Status: "DEPRIORITIZED", Regions: []string{"EU", "US"}, DocsURL: "https://docs.developers.kaspr.io/"},
	{Slug: "contactout", New: ContactOut, Category: "email-find", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://api.contactout.com/"},
	{Slug: "findymail", New: Findymail, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://app.findymail.com/docs/"},
	{Slug: "voila-norbert", New: VoilaNorbert, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.voilanorbert.com/api/"},
	{Slug: "surfe", NewAsync: Surfe, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.surfe.com/"},
	{Slug: "lemlist", NewAsync: Lemlist, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developer.lemlist.com/api-reference/endpoints/enrich/enrich-data"},
	// Async email finders (ADR-0024 submit→poll) — registered via NewAsync.
	{Slug: "dropcontact", NewAsync: Dropcontact, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"EU", "global"}, DocsURL: "https://developer.dropcontact.com/"},
	{Slug: "icypeas", NewAsync: Icypeas, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://api-doc.icypeas.com/getting-started/"},
	{Slug: "enrow", NewAsync: Enrow, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.enrow.io/api-reference/email-finder/find-single"},
	{Slug: "snov", NewAsync: Snov, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://snov.io/api"},
	// Wave 7 (coverage-audit gap-fill) — L2 email-find long tail.
	{Slug: "leadmagic", New: LeadMagic, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://leadmagic.io/docs/v1"},
	{Slug: "getprospect", New: GetProspect, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://getprospect.readme.io/"},
	{Slug: "skrapp", New: Skrapp, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://skrapp.io/api"},
	{Slug: "tomba", New: Tomba, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.tomba.io/api/finder"},
	{Slug: "cufinder", New: Cufinder, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://apidoc.cufinder.io/"},
	// Wave 8 (residual audit) — L2 email-find / contact.
	{Slug: "uplead", New: UpLead, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.uplead.com/"},
	{Slug: "adapt-io", New: AdaptIO, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.adapt.io/api-docs/v3/"},
	{Slug: "aeroleads", New: AeroLeads, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://aeroleads.com/blog/how-to-use-aeroleads-api/"},
	{Slug: "signalhire", New: SignalHire, Category: "email-find", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.signalhire.com/person-api/retrieve-person"},
	{Slug: "wiza", NewAsync: Wiza, Category: "email-find", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.wiza.co/"},
	{Slug: "rocketreach", NewAsync: RocketReach, Category: "email-find", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.rocketreach.co/reference/people-lookup-api"},

	// L9 — Waterfall orchestration aggregators (async submit→poll).
	{Slug: "bettercontact", NewAsync: BetterContact, Category: "orchestration", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://doc.bettercontact.rocks/"},
	{Slug: "fullenrich", NewAsync: FullEnrich, Category: "orchestration", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.fullenrich.com/"},
	{Slug: "anymailfinder", New: AnymailFinder, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://anymailfinder.com/email-finder-api/docs/find-person-email"},
	{Slug: "datagma", New: Datagma, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://datagmaapi.readme.io/reference/find-work-email-address"},

	// L3 — Email verification.
	{Slug: "neverbounce", New: NeverBounce, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.neverbounce.com/reference/single-check"},
	{Slug: "kickbox", New: Kickbox, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.kickbox.com/docs/using-the-api"},
	{Slug: "zerobounce", New: ZeroBounce, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-validate-emails"},
	{Slug: "emailable", New: Emailable, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://emailable.com/docs/api"},
	{Slug: "bouncer", New: Bouncer, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.usebouncer.com/api-reference/real-time/verify-email"},
	{Slug: "millionverifier", New: MillionVerifier, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developer.millionverifier.com/"},
	{Slug: "verifalia", NewAsync: Verifalia, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://verifalia.com/developers"},
	{Slug: "bounceban", New: BounceBan, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://bounceban.com/public/doc/api.html"},
	{Slug: "scrubby", New: Scrubby, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.scrubby.io/"},
	{Slug: "enrichley", New: Enrichley, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.enrichley.io/"},
	{Slug: "mailfloss", New: Mailfloss, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.mailfloss.com/"},
	{Slug: "debounce", New: DeBounce, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.debounce.com/api-reference/endpoint/single-validation"},
	{Slug: "clearout", New: Clearout, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.clearout.io/developers/api/email-verify"},
	{Slug: "mailgun-validate", New: MailgunValidate, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"us", "eu"}, DocsURL: "https://documentation.mailgun.com/docs/validate/"},
	{Slug: "quickemailverification", New: QuickEmailVerification, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.quickemailverification.com/email-verification-api/verify-an-email-address"},
	{Slug: "myemailverifier", New: MyEmailVerifier, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://github.com/pat-myemailverifier/myemailverifier-api"},
	{Slug: "mailboxvalidator", New: MailboxValidator, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.mailboxvalidator.com/api-single-validation"},
	{Slug: "bouncify", New: Bouncify, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://bouncify.readme.io/reference/single-validation-api"},
	{Slug: "emaillistverify", New: EmailListVerify, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://api.emaillistverify.com/api-doc"},
	{Slug: "cloudmersive", New: Cloudmersive, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://api.cloudmersive.com/docs/validate.asp"},
	{Slug: "abstract-email", New: AbstractEmail, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.abstractapi.com/api/email-validation"},
	{Slug: "mailercheck", New: MailerCheck, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.mailercheck.com/email"},
	{Slug: "reoon", New: Reoon, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.reoon.com/articles/api-documentation-of-reoon-email-verifier/"},
	{Slug: "mails-so", New: MailsSo, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.mails.so/intro"},
	{Slug: "emailhippo", New: EmailHippo, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://email-verify-api-docs.readthedocs.io/en/latest/"},
	{Slug: "truelist", New: Truelist, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://truelist.io/docs/api"},
	{Slug: "verimail", New: Verimail, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://verimail.io/docs/v3"},
	{Slug: "sendpulse-verifier", NewAsync: SendPulseVerifier, Category: "email-verify", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://sendpulse.com/integrations/api/verifier"},

	// L5 — Phone validation.
	{Slug: "twilio-lookup", New: Twilio, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.twilio.com/docs/lookup/v2-api"},
	{Slug: "telnyx", New: Telnyx, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.telnyx.com/docs/identity/number-lookup"},
	{Slug: "vonage", New: Vonage, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developer.vonage.com/en/api/number-insight"},
	{Slug: "messagebird", New: MessageBird, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.messagebird.com/api/lookup/"},
	{Slug: "ipqualityscore", New: IPQualityScore, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.ipqualityscore.com/documentation/phone-number-validation-api/overview"},
	{Slug: "plivo", New: Plivo, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.plivo.com/docs/lookup/"},
	{Slug: "infobip", New: Infobip, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.infobip.com/docs/number-lookup"},
	{Slug: "numverify", New: NumVerify, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.apilayer.com/numverify"},
	{Slug: "abstract-phone", New: AbstractPhone, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.abstractapi.com/api/phone-validation"},
	{Slug: "veriphone", New: Veriphone, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://veriphone.io/docs/v2"},
	{Slug: "byteplant-phone", New: Byteplant, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.byteplant.com/phone-validator/api.html"},
	{Slug: "telesign", New: Telesign, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developer.telesign.com/enterprise/docs/phone-id-get-started"},
	{Slug: "realphonevalidation", New: RealPhoneValidation, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"US"}, DocsURL: "https://realphonevalidation.com/api-documentation/turbo-v3-api-doc/"},
	{Slug: "trestle", New: Trestle, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"US", "global"}, DocsURL: "https://docs.trestleiq.com/api-reference/phone-validation-api"},
	{Slug: "numlookupapi", New: NumLookupAPI, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://numlookupapi.com/docs/validate"},
	{Slug: "neutrinoapi", New: NeutrinoAPI, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.neutrinoapi.com/api/phone-validate/"},

	// L4 — Phone / contact finding.
	{Slug: "salesintel", New: SalesIntel, Category: "phone-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"US", "global"}, DocsURL: "https://developer.salesintel.io/salesintel-api-documentation/people-contact-apis"},

	// L6 — Firmographics.
	{Slug: "clearbit", New: Clearbit, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://dashboard.clearbit.com/docs"},
	{Slug: "brandfetch", New: Brandfetch, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.brandfetch.com/brand-api/overview"},
	{Slug: "crunchbase", New: Crunchbase, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://data.crunchbase.com/docs/using-entity-lookup-apis"},
	{Slug: "opencorporates", New: OpenCorporates, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://api.opencorporates.com/documentation/API-Reference"},
	{Slug: "ocean-io", New: OceanIO, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://app.ocean.io/docs/enrich/enrichCompany"},
	{Slug: "the-companies-api", New: TheCompaniesAPI, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.thecompaniesapi.com/api/enrich-company-from-domain"},
	{Slug: "storeleads", New: Storeleads, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://storeleads.app/api"},
	{Slug: "diffbot", New: Diffbot, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.diffbot.com/reference/enhanceget"},
	{Slug: "vainu", New: Vainu, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"EU", "global"}, DocsURL: "https://developers.vainu.com/reference/listcompanies"},
	{Slug: "global-database", New: GlobalDatabase, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://api.globaldatabase.com/docs/v2/"},
	{Slug: "data-axle", New: DataAxle, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"US", "CA"}, DocsURL: "https://platform.data-axle.com/places/docs/match_api_v2"},
	{Slug: "owler", New: Owler, Category: "firmographics", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://developers.owler.com/docs"},
	{Slug: "leadspace", New: Leadspace, Category: "firmographics", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://support.leadspace.com/hc/en-us/articles/115006003389"},
	{Slug: "ninjapear", New: NinjaPear, Category: "firmographics", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://nubela.co/docs"},
	{Slug: "mixrank", New: MixRank, Category: "firmographics", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://mixrank.com/api/documentation"},
	// Async (ADR-0024): D&B is match→fetch + oauth2-cc — registered via NewAsync.
	{Slug: "dnb", NewAsync: DNB, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://directplus.documentation.dnb.com/"},
	{Slug: "explorium", NewAsync: Explorium, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developers.explorium.ai/reference/firmographics"},
	{Slug: "endole", NewAsync: Endole, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"GB"}, DocsURL: "https://www.endole.co.uk/developers/dashboard/api/documentation/"},
	{Slug: "demandbase", NewAsync: Demandbase, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://developer.demandbase.com/reference/companyfetch"},
	{Slug: "cleanlist", New: Cleanlist, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.cleanlist.ai/mcp-api/enrichment"},
	{Slug: "infobelpro", New: InfobelPRO, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://getdata.infobelpro.com/Help"},
	{Slug: "abstract-company", New: AbstractCompany, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.abstractapi.com/api/company-enrichment"},
	{Slug: "extruct", New: Extruct, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.extruct.ai/api-reference/company-lookup"},
	{Slug: "companyenrich", New: CompanyEnrich, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.companyenrich.com/reference/get_companies-enrich"},
	{Slug: "companies-house", NewAsync: CompaniesHouse, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"GB"}, DocsURL: "https://developer.company-information.service.gov.uk/"},
	{Slug: "bigpicture", New: BigPicture, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.bigpicture.io/api/"},
	{Slug: "brreg", NewAsync: Brreg, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"NO"}, DocsURL: "https://data.brreg.no/enhetsregisteret/api/dokumentasjon/en/index.html"},
	{Slug: "gleif", New: GLEIF, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://documenter.getpostman.com/view/7679680/SVYrrxuU"},
	{Slug: "recherche-entreprises", New: RechercheEntreprises, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"FR"}, DocsURL: "https://recherche-entreprises.api.gouv.fr/docs/"},
	{Slug: "north-data", New: NorthData, Category: "firmographics", Status: "DEPRIORITIZED", Regions: []string{"DE", "EU"}, DocsURL: "https://github.com/northdata/api/blob/master/doc/data-api-userguide/data-api-userguide.md"},
	{Slug: "nz-companies", NewAsync: NZCompanies, Category: "firmographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"NZ"}, DocsURL: "https://portal.api.business.govt.nz/api/nzbn"},
	{Slug: "opensanctions", New: OpenSanctions, Category: "firmographics", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://www.opensanctions.org/docs/api/"},
	{Slug: "predictleads", New: PredictLeads, Category: "technographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.predictleads.com/v3"},

	// L1 — Identity resolution / contact append.
	{Slug: "pipl", New: Pipl, Category: "identity", Status: "DEPRIORITIZED", Regions: []string{"global", "US"}, DocsURL: "https://docs.pipl.com/"},
	{Slug: "versium", New: Versium, Category: "identity", Status: "DEPRIORITIZED", Regions: []string{"US"}, DocsURL: "https://api-documentation.versium.com/reference/contact-append-api"},
	{Slug: "reverse-contact", New: ReverseContact, Category: "identity", Status: "DEPRIORITIZED", Regions: []string{"global"}, DocsURL: "https://docs.reversecontact.com"},

	// L7 — Technographics.
	{Slug: "builtwith", New: BuiltWith, Category: "technographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://api.builtwith.com/domain-api"},
	{Slug: "theirstack", New: TheirStack, Category: "technographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://theirstack.com/en/docs/api-reference"},
	{Slug: "wappalyzer", New: Wappalyzer, Category: "technographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.wappalyzer.com/docs/api/v2/lookup/"},
	{Slug: "hg-insights", New: HGInsights, Category: "technographics", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://data-docs.hginsights.com/v2/guides/enrichment"},

	// L8 — Intent / signals.
	{Slug: "g2", New: G2, Category: "intent", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://data.g2.com/api/v2/docs/index.html"},
	{Slug: "6sense", New: SixSense, Category: "intent", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://api.6sense.com/docs/"},
}

// Registry returns the append-only list of registered providers.
func Registry() []Registered { return registry }

// Construct builds the entry's adapter (sync or async) as a provider.Introspectable — the common
// surface the engine wiring (Adapter), the SSRF allow-list, and the catalog seeder all read. base=""
// selects the production default BaseURL. Panics only if an entry has neither New nor NewAsync,
// which the registry invariant test forbids.
func (r Registered) Construct(base string, c *http.Client) provider.Introspectable {
	if r.NewAsync != nil {
		return r.NewAsync(base, c)
	}
	return r.New(base, c)
}

// All constructs every registered adapter against the shared egress client. This is what the
// enrich binaries wire into engine.New / router.New in place of the old mock slice. One egress
// client serves every adapter: the per-request AuthDescriptor (carried on the request context)
// tells the injector which key pool to lease, so a single injector authenticates all providers.
func All(c *http.Client) []provider.Adapter {
	out := make([]provider.Adapter, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.Construct("", c))
	}
	return out
}

// Hosts returns the distinct default base-URL hostnames of every registered adapter, for
// building the egress SSRF allow-list (provider.NewHostAllowList). Constructing with a nil
// client is safe here — Hosts only reads BaseURL, it never performs a Fetch.
func Hosts() []string {
	seen := make(map[string]struct{})
	hosts := make([]string, 0, len(registry))
	for _, r := range registry {
		a := r.Construct("", nil)
		u, err := url.Parse(a.Base())
		if err != nil || u.Hostname() == "" {
			continue
		}
		if _, ok := seen[u.Hostname()]; ok {
			continue
		}
		seen[u.Hostname()] = struct{}{}
		hosts = append(hosts, u.Hostname())
	}
	return hosts
}
