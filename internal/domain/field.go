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

	// Person-name match keys (inputs for email-finder providers).
	FieldFirstName Field = "first_name"
	FieldLastName  Field = "last_name"
	FieldFullName  Field = "full_name"
)

// canonicalFields is the allow-list used by Field.Valid. It mirrors the constants
// above; a value produced or requested outside this set is rejected at the edges so
// typos never reach a provider call or the datastore.
var canonicalFields = map[Field]struct{}{
	FieldWorkEmail: {}, FieldPersonalEmail: {}, FieldEmailStatus: {},
	FieldMobilePhone: {}, FieldDirectDial: {}, FieldOfficePhone: {}, FieldPhoneStatus: {},
	FieldLinkedInURL: {}, FieldJobTitle: {}, FieldSeniority: {}, FieldDepartment: {},
	FieldCompanyDomain: {}, FieldCompanyName: {}, FieldEmployeeCount: {}, FieldIndustry: {},
	FieldFirstName: {}, FieldLastName: {}, FieldFullName: {},
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
