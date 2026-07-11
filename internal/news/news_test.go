package news

import (
	"testing"
	"time"
)

// parseTime must decode the Postgres ISO timestamptz text forms (with/without fractional seconds and
// with a 2- or 4-digit offset) as well as RFC3339, and return the zero Time on anything unparseable.
func TestParseTime(t *testing.T) {
	want := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	cases := []string{
		"2026-07-01 12:00:00+00",        // Postgres ISO, no fraction, 2-digit offset
		"2026-07-01 12:00:00.000000+00", // Postgres ISO, fraction
		"2026-07-01 12:00:00+00:00",     // 4-digit offset
		"2026-07-01T12:00:00Z",          // RFC3339
	}
	for _, s := range cases {
		got := parseTime(s)
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", s, got, want)
		}
	}
	if !parseTime("").IsZero() {
		t.Error("empty string must parse to the zero Time")
	}
	if !parseTime("not-a-time").IsZero() {
		t.Error("unparseable string must parse to the zero Time")
	}
}
