package domain

import "testing"

// TestField_Valid_RIScalars locks the canonical vocabulary at 39 (33 + the six R&I scalars added
// DOC-FIRST per ADR-0028) and proves each new Field is accepted by the edge gate while an unknown
// name is rejected. A change to the vocabulary size must be intentional and update this count.
func TestField_Valid_RIScalars(t *testing.T) {
	riScalars := []Field{
		FieldTwitterURL, FieldFacebookURL, FieldGitHubURL,
		FieldCrunchbaseURL, FieldCompanyTicker, FieldTotalFundingUSD,
	}
	for _, f := range riScalars {
		if !f.Valid() {
			t.Errorf("%s should be a canonical Field", f)
		}
	}
	if Field("not_a_real_field").Valid() {
		t.Error("an unknown Field name must be rejected by Valid()")
	}
	if got, want := len(canonicalFields), 39; got != want {
		t.Errorf("canonical vocabulary size = %d, want %d (33 base + 6 R&I scalars)", got, want)
	}
}
