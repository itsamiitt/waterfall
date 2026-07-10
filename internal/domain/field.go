// Package domain holds the core entities of the Waterfall Enrichment Engine.
//
// Names here are the canonical vocabulary defined in docs/00-Project-Overview.md §7.
// Nothing in this package may import other internal packages: it is the shared
// language every other module speaks.
package domain

// Field is a single enrichable datum, snake_case, drawn from the canonical Field
// vocabulary (docs/00 §7). It is a named string so the type system distinguishes a
// Field from an arbitrary string.
type Field string

// Canonical Field vocabulary (docs/00 §7). Extend this list only alongside the doc.
const (
	FieldWorkEmail     Field = "work_email"
	FieldPersonalEmail Field = "personal_email"
	FieldEmailStatus   Field = "email_status"
	FieldMobilePhone   Field = "mobile_phone"
	FieldDirectDial    Field = "direct_dial"
	FieldOfficePhone   Field = "office_phone"
	FieldPhoneStatus   Field = "phone_status"
	FieldLinkedInURL   Field = "linkedin_url"
	FieldJobTitle      Field = "job_title"
	FieldSeniority     Field = "seniority"
	FieldDepartment    Field = "department"
	FieldCompanyDomain Field = "company_domain"
	FieldCompanyName   Field = "company_name"
	FieldEmployeeCount Field = "employee_count"
	FieldIndustry      Field = "industry"

	// Firmographics (L6). Company-level attributes; enriched once per Company, cached and
	// shared across that Company's contacts. Names match docs/00 §7 (ADR-0023 rollout).
	FieldCompanyRevenue     Field = "company_revenue"
	FieldFundingStage       Field = "funding_stage"
	FieldCompanyFoundedYear Field = "company_founded_year"
	FieldCompanyHQCountry   Field = "company_hq_country"
	FieldCompanyHQCity      Field = "company_hq_city"
	FieldCompanyType        Field = "company_type"
	FieldCompanyLinkedInURL Field = "company_linkedin_url"
	FieldCompanyPhone       Field = "company_phone"
	FieldNAICS              Field = "naics"
	FieldSIC                Field = "sic"
	FieldDUNS               Field = "duns_number"

	// Technographics (L7). The detected tech stack is inherently multi-valued but is stored
	// as a SINGLE normalized Observation value — a sorted, deduped, comma-joined list — so no
	// field_versions schema change is needed (one value per Field; ADR-0023).
	FieldTechnographics Field = "technographics"

	// Intent / signals (L8). Account- or contact-level buying signals.
	FieldIntentTopics Field = "intent_topics" // normalized comma-joined topic list
	FieldIntentScore  Field = "intent_score"  // numeric intent strength, stringified
	FieldBuyingSignal Field = "buying_signal" // event type: job_change | funding | hiring | ...

	// Person-name match keys (inputs for email-finder providers).
	FieldFirstName Field = "first_name"
	FieldLastName  Field = "last_name"
	FieldFullName  Field = "full_name"

	// Research & Intelligence scalars (R&I; ADR-0028, docs/00 §7). Each is genuinely
	// single-valued, so it slots into field_versions with no schema change. Multi-valued or
	// relational R&I data (competitors, acquisitions, funding rounds, partnerships, locations)
	// stays Dossier-only and is NEVER a canonical Field.
	FieldTwitterURL      Field = "twitter_url"
	FieldFacebookURL     Field = "facebook_url"
	FieldGitHubURL       Field = "github_url"
	FieldCrunchbaseURL   Field = "crunchbase_url"
	FieldCompanyTicker   Field = "company_ticker"
	FieldTotalFundingUSD Field = "total_funding_usd"
)

// canonicalFields is the allow-list used by Field.Valid. It mirrors the constants
// above; a value produced or requested outside this set is rejected at the edges so
// typos never reach a provider call or the datastore.
var canonicalFields = map[Field]struct{}{
	FieldWorkEmail: {}, FieldPersonalEmail: {}, FieldEmailStatus: {},
	FieldMobilePhone: {}, FieldDirectDial: {}, FieldOfficePhone: {}, FieldPhoneStatus: {},
	FieldLinkedInURL: {}, FieldJobTitle: {}, FieldSeniority: {}, FieldDepartment: {},
	FieldCompanyDomain: {}, FieldCompanyName: {}, FieldEmployeeCount: {}, FieldIndustry: {},
	// Firmographics (L6).
	FieldCompanyRevenue: {}, FieldFundingStage: {}, FieldCompanyFoundedYear: {},
	FieldCompanyHQCountry: {}, FieldCompanyHQCity: {}, FieldCompanyType: {},
	FieldCompanyLinkedInURL: {}, FieldCompanyPhone: {}, FieldNAICS: {}, FieldSIC: {}, FieldDUNS: {},
	// Technographics (L7) + Intent/signals (L8).
	FieldTechnographics: {}, FieldIntentTopics: {}, FieldIntentScore: {}, FieldBuyingSignal: {},
	FieldFirstName: {}, FieldLastName: {}, FieldFullName: {},
	// Research & Intelligence scalars (R&I; ADR-0028).
	FieldTwitterURL: {}, FieldFacebookURL: {}, FieldGitHubURL: {}, FieldCrunchbaseURL: {},
	FieldCompanyTicker: {}, FieldTotalFundingUSD: {},
}

// Valid reports whether f is part of the canonical Field vocabulary.
func (f Field) Valid() bool {
	_, ok := canonicalFields[f]
	return ok
}

// Confidence is a calibrated probability in [0,1] that a Field value is correct
// (docs/00 §7; methodology ADR-0005). Values outside the range are clamped by Clamp
// rather than trusted, because a miscalibrated provider must never push a score past 1.
type Confidence float64

// Clamp constrains c to the closed interval [0,1].
func (c Confidence) Clamp() Confidence {
	switch {
	case c < 0:
		return 0
	case c > 1:
		return 1
	default:
		return c
	}
}

// Credits is a provider-cost unit (docs/16). It is integer-valued to keep cost
// accounting (G4) exact: floating-point credits would let rounding erode a ceiling.
type Credits int64
