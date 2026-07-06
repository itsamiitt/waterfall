package alerts

import "testing"

func i64p(n int64) *int64 { return &n }

// TestAnomalyFloor_Default confirms a rule with NO per-rule floor falls back to the package
// default (anomalyAbsFloor = 1000 credits, OI-P6-3 / doc 10 §4).
func TestAnomalyFloor_Default(t *testing.T) {
	if got := anomalyFloor(Rule{}); got != anomalyAbsFloor {
		t.Fatalf("default floor = %v, want %v", got, float64(anomalyAbsFloor))
	}
	if got := anomalyFloor(Rule{AnomalyFloorCredits: i64p(5000)}); got != 5000 {
		t.Fatalf("per-rule floor = %v, want 5000", got)
	}
}

// TestAnomalyBreaches_PerRuleFloor is the OI-P6-3 acceptance test: a rule whose absolute floor is
// 5000 credits does NOT fire on a 2000-credit delta (even though the percent threshold is met),
// while the default rule (floor 1000) fires on that same >1000-credit delta.
func TestAnomalyBreaches_PerRuleFloor(t *testing.T) {
	const pct = 120.0 // percent increase over median — well past the 50% threshold

	// Default rule (nil floor → 1000): a 2000-credit delta clears the floor AND the percent → fire.
	def := Rule{Threshold: 50}
	if !anomalyBreaches(def, pct, 2000) {
		t.Fatalf("default rule should fire at a 2000-credit delta with pct=%v > threshold", pct)
	}
	// But a delta at/under the default floor does NOT fire even with a huge percent.
	if anomalyBreaches(def, pct, 900) {
		t.Fatalf("default rule must not fire below the 1000-credit floor")
	}

	// A rule that sets floor=5000 does NOT fire at a 2000-credit delta despite the percent breach.
	high := Rule{Threshold: 50, AnomalyFloorCredits: i64p(5000)}
	if anomalyBreaches(high, pct, 2000) {
		t.Fatalf("floor=5000 rule must NOT fire at a 2000-credit delta")
	}
	// The same rule DOES fire once the delta clears its raised floor.
	if !anomalyBreaches(high, pct, 6000) {
		t.Fatalf("floor=5000 rule should fire at a 6000-credit delta")
	}

	// The percent threshold is still a hard gate regardless of the floor: a big delta with a
	// sub-threshold percent does not fire.
	if anomalyBreaches(def, 10, 100000) {
		t.Fatalf("a sub-threshold percent must not fire regardless of delta")
	}
}
