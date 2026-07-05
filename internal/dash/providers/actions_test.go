package providers

import (
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func timePtr() *time.Time { t := time.Now(); return &t }

// TestCanTransition pins the op_state valid-transition guard: any state may move to any OTHER
// state, but re-issuing the current state (a no-op) is rejected, and unknown states never move.
func TestCanTransition(t *testing.T) {
	states := []string{OpEnabled, OpDisabled, OpPaused, OpMaintenance}
	for _, from := range states {
		for _, to := range states {
			got := canTransition(from, to)
			want := from != to
			if got != want {
				t.Errorf("canTransition(%q,%q)=%v, want %v", from, to, got, want)
			}
		}
	}
	if canTransition("bogus", OpEnabled) {
		t.Error("transition from unknown state must be rejected")
	}
	if canTransition(OpEnabled, "bogus") {
		t.Error("transition to unknown state must be rejected")
	}
}

// TestRankBy_OrderingAndNullsLast verifies descending score order with NULL scores sorted last
// and deterministic id tie-breaks; and ascending order for the cost metric.
func TestRankBy_OrderingAndNullsLast(t *testing.T) {
	providers := []Provider{
		{ID: "a", HealthScore: f64(0.5), CostScore: f64(3)},
		{ID: "b", HealthScore: f64(0.9), CostScore: f64(1)},
		{ID: "c", HealthScore: nil, CostScore: f64(2)}, // NULL health -> last
		{ID: "d", HealthScore: f64(0.9), CostScore: f64(1)},
	}

	got := rankBy(providers, "health_score")
	order := []string{got[0].ID, got[1].ID, got[2].ID, got[3].ID}
	// 0.9 tie (b,d) breaks by id -> b then d; then 0.5 (a); then NULL (c).
	want := []string{"b", "d", "a", "c"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("health ranking order = %v, want %v", order, want)
		}
	}
	if got[0].Rank != 1 || got[3].Rank != 4 {
		t.Errorf("ranks not 1..n: %+v", got)
	}

	// Cost is ascending (smaller is better): b/d (1) before c (2) before a (3).
	cost := rankBy(providers, "cost_score")
	if cost[0].ID != "b" || cost[len(cost)-1].ID != "a" {
		t.Errorf("cost ranking = %v, want b..a", []string{cost[0].ID, cost[1].ID, cost[2].ID, cost[3].ID})
	}

	// Unknown metric falls back to health_score.
	if fb := rankBy(providers, "nope"); fb[0].ID != "b" {
		t.Errorf("unknown metric did not fall back to health_score: top=%s", fb[0].ID)
	}
}

// TestCoverage_GroupsAndGrid checks per-group coverage percentages, archived exclusion, and the
// field×provider grid.
func TestCoverage_GroupsAndGrid(t *testing.T) {
	now := timePtr()
	providers := []Provider{
		{ID: "email1", Capabilities: []Capability{{Field: "work_email"}}},
		{ID: "email2", Capabilities: []Capability{{Field: "personal_email"}, {Field: "email_status"}}},
		{ID: "phone1", Capabilities: []Capability{{Field: "mobile_phone"}}},
		{ID: "firmo1", Capabilities: []Capability{{Field: "company_domain"}, {Field: "job_title"}}},
		{ID: "arch", ArchivedAt: now, Capabilities: []Capability{{Field: "work_email"}}}, // excluded
	}

	rep := coverage(providers)
	if rep.Total != 4 {
		t.Fatalf("total = %d, want 4 (archived excluded)", rep.Total)
	}
	// email: email1, email2 => 2/4 = 50%.
	if g := rep.Groups["email"]; g.ProvidersCovering != 2 || g.Pct != 50 {
		t.Errorf("email coverage = %+v, want 2 / 50%%", g)
	}
	// phone: phone1 => 1/4 = 25%.
	if g := rep.Groups["phone"]; g.ProvidersCovering != 1 || g.Pct != 25 {
		t.Errorf("phone coverage = %+v, want 1 / 25%%", g)
	}
	// firmographic: firmo1 => 1/4 = 25%.
	if g := rep.Groups["firmographic"]; g.ProvidersCovering != 1 || g.Pct != 25 {
		t.Errorf("firmographic coverage = %+v, want 1 / 25%%", g)
	}
	// Grid: work_email declared only by the live email1 (archived arch excluded).
	if ids := rep.Grid["work_email"]; len(ids) != 1 || ids[0] != "email1" {
		t.Errorf("work_email grid = %v, want [email1]", ids)
	}
}

// TestCoverage_EmptyCatalog is the divide-by-zero guard.
func TestCoverage_EmptyCatalog(t *testing.T) {
	rep := coverage(nil)
	if rep.Total != 0 || rep.Groups["email"].Pct != 0 {
		t.Errorf("empty catalog coverage = %+v, want zero", rep)
	}
}

// TestCompareEntries_PreservesFieldsAndAvailability spot-checks the compare projection.
func TestCompareEntries_PreservesFieldsAndAvailability(t *testing.T) {
	in := []Provider{
		{ID: "x", DisplayName: "X", Status: StatusActiveCandidate, OpState: OpEnabled,
			Capabilities: []Capability{{Field: "work_email", CostCredits: 1, ExpectedConfidence: 0.9}},
			HealthScore:  f64(0.98)},
		{ID: "y", DisplayName: "Y", Status: StatusActiveCandidate, OpState: OpPaused},
	}
	out := compareEntries(in)
	if len(out) != 2 {
		t.Fatalf("compare entries = %d, want 2", len(out))
	}
	if !out[0].EffectiveAvailable || out[0].Availability != string(Available) {
		t.Errorf("entry x should be available: %+v", out[0])
	}
	if out[1].EffectiveAvailable || out[1].Availability != string(Unavailable) {
		t.Errorf("entry y (paused) should be unavailable: %+v", out[1])
	}
	if len(out[0].Declared) != 1 || out[0].Declared[0].Field != "work_email" {
		t.Errorf("entry x declared capabilities lost: %+v", out[0].Declared)
	}
}
