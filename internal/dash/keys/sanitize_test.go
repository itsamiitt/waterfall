package keys

import "testing"

func TestSanitizeCell_FormulaInjection(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"=cmd|'/c calc'!A1", "'=cmd|'/c calc'!A1"},
		{"=1+1", "'=1+1"},
		{"+41", "'+41"},
		{"-2", "'-2"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\tTAB", "'\tTAB"},
		{"\rCR", "'\rCR"},
		{"\nLF", "'\nLF"},
		{"hunter-prod-07", "hunter-prod-07"}, // benign (leading 'h')
		{"us", "us"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeCell(c.in); got != c.want {
			t.Errorf("sanitizeCell(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeCell_Idempotentish(t *testing.T) {
	// After escaping, a value must no longer be treated as a formula, and re-escaping is a no-op.
	for _, in := range []string{"=cmd", "+x", "-y", "@z"} {
		once := sanitizeCell(in)
		if isDangerousCell(once) {
			t.Errorf("sanitizeCell(%q) = %q still dangerous", in, once)
		}
		if twice := sanitizeCell(once); twice != once {
			t.Errorf("re-escape of %q changed it to %q", once, twice)
		}
	}
}

func TestIsDangerousCell(t *testing.T) {
	if !isDangerousCell("=x") || isDangerousCell("safe") || isDangerousCell("") {
		t.Fatal("isDangerousCell classification wrong")
	}
}
