package calibrate

import (
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
)

func TestIsotonic_Monotonic(t *testing.T) {
	// Deliberately non-monotonic raw labels; PAVA must produce a non-decreasing fit.
	m := FitIsotonic([]Point{
		{X: 0.1, Y: 0}, {X: 0.2, Y: 1}, {X: 0.3, Y: 0}, {X: 0.4, Y: 1}, {X: 0.5, Y: 1},
	})
	prev := -1.0
	for x := 0.0; x <= 1.0; x += 0.05 {
		p := m.Predict(x)
		if p < prev-1e-9 {
			t.Fatalf("calibration not monotonic at x=%.2f: %.3f < %.3f", x, p, prev)
		}
		if p < 0 || p > 1 {
			t.Fatalf("calibrated prob out of range: %.3f", p)
		}
		prev = p
	}
}

func TestIsotonic_CorrectsOverconfidence(t *testing.T) {
	// A provider that reports 0.9 but is right only 2/5 of the time.
	m := FitIsotonic([]Point{
		{X: 0.5, Y: 0}, {X: 0.5, Y: 0}, {X: 0.5, Y: 1},
		{X: 0.9, Y: 1}, {X: 0.9, Y: 1}, {X: 0.9, Y: 0}, {X: 0.9, Y: 0}, {X: 0.9, Y: 0},
	})
	got := m.Predict(0.9)
	if got < 0.3 || got > 0.5 {
		t.Fatalf("0.9 should calibrate to ~0.4, got %.3f", got)
	}
	if m.Predict(0.5) > m.Predict(0.9) {
		t.Fatal("calibration must remain monotonic across score levels")
	}
}

func TestCalibrator_OptInPerPair(t *testing.T) {
	c := New()
	c.Fit("acme", domain.FieldWorkEmail, []Point{
		{X: 0.9, Y: 1}, {X: 0.9, Y: 0}, {X: 0.9, Y: 0}, // 0.9 -> ~0.33
	})
	if got := c.Calibrate("acme", domain.FieldWorkEmail, 0.9); got >= 0.9 {
		t.Fatalf("calibrated confidence should drop below raw 0.9, got %.3f", got)
	}
	// An uncalibrated pair passes through unchanged.
	if got := c.Calibrate("other", domain.FieldWorkEmail, 0.9); got != 0.9 {
		t.Fatalf("uncalibrated pair must be identity, got %.3f", got)
	}
	// A nil calibrator is identity (defensive).
	var nilCal *Calibrator
	if got := nilCal.Calibrate("x", domain.FieldWorkEmail, 0.7); got != 0.7 {
		t.Fatalf("nil calibrator must be identity, got %.3f", got)
	}
}
