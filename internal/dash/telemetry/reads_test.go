package telemetry

import (
	"errors"
	"testing"
	"time"
)

// TestCheckWindow pins the bounded-read guard (doc 03 §4 / doc 04 §1.4): inverted windows and
// windows reaching beyond retention are rejected with ErrWindowOutOfRange; an in-range window
// passes.
func TestCheckWindow(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	ret := 7 * day // provider_stats_1m retention

	// In-range: last 6 days.
	if err := checkWindow(now.AddDate(0, 0, -6), now, now, ret); err != nil {
		t.Errorf("in-range window rejected: %v", err)
	}
	// Exactly at the horizon is allowed.
	if err := checkWindow(now.Add(-ret), now, now, ret); err != nil {
		t.Errorf("at-horizon window rejected: %v", err)
	}
	// Beyond retention: rejected.
	if err := checkWindow(now.Add(-ret-time.Hour), now, now, ret); !errors.Is(err, ErrWindowOutOfRange) {
		t.Errorf("beyond-retention window = %v, want ErrWindowOutOfRange", err)
	}
	// Inverted (to <= from): rejected.
	if err := checkWindow(now, now.Add(-time.Hour), now, ret); !errors.Is(err, ErrWindowOutOfRange) {
		t.Errorf("inverted window = %v, want ErrWindowOutOfRange", err)
	}
	// Zero-width (to == from): rejected.
	if err := checkWindow(now, now, now, ret); !errors.Is(err, ErrWindowOutOfRange) {
		t.Errorf("zero-width window = %v, want ErrWindowOutOfRange", err)
	}
}

// TestRetentionMatrix guards the per-resolution retention values against the doc 03 §4 matrix.
func TestRetentionMatrix(t *testing.T) {
	if providerStatsRetention(Res1m) != 7*day || providerStatsRetention(Res1h) != 90*day || providerStatsRetention(Res1d) != 730*day {
		t.Errorf("provider_stats retention mismatch")
	}
	if keyUsageRetention(Res1m) != 3*day || keyUsageRetention(Res1h) != 30*day || keyUsageRetention(Res1d) != 365*day {
		t.Errorf("key_usage retention mismatch")
	}
}
