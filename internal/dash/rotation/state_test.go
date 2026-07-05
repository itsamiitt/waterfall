package rotation

import (
	"testing"

	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/dash/keys"
)

// row builds a poolKeyRow with an optional priority.
func row(id string, weight int, prio *int64, status string) poolKeyRow {
	return poolKeyRow{ID: id, EnvelopeID: "env-" + id, Weight: weight, Priority: prio, Status: status}
}

func i64p(v int64) *int64 { return &v }

func TestKeyAvailable(t *testing.T) {
	cases := map[string]bool{
		keys.StatusActive:      true,
		keys.StatusRotating:    true,
		keys.StatusPaused:      false,
		keys.StatusExhausted:   false,
		keys.StatusRateLimited: false,
		keys.StatusAuthFailed:  false,
		keys.StatusDisabled:    false,
		keys.StatusExpired:     false,
		keys.StatusArchived:    false,
		"nonsense":             false,
	}
	for status, want := range cases {
		if got := KeyAvailable(status); got != want {
			t.Errorf("KeyAvailable(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestRoundRobinCyclesAvailableOnly(t *testing.T) {
	rows := []poolKeyRow{
		row("a", 100, nil, keys.StatusActive),
		row("b", 100, nil, keys.StatusDisabled), // unavailable — must be skipped
		row("c", 100, nil, keys.StatusActive),
	}
	ps := buildPoolState("hunter:default", "round_robin", "", rows, bandit.New())

	seen := map[string]int{}
	for i := 0; i < 100; i++ {
		k, ok := ps.Select("")
		if !ok {
			t.Fatal("round_robin returned no key with available members")
		}
		seen[k.id]++
	}
	if seen["b"] != 0 {
		t.Fatalf("round_robin selected the disabled key b %d times", seen["b"])
	}
	if seen["a"] == 0 || seen["c"] == 0 {
		t.Fatalf("round_robin did not cycle over both available keys: %v", seen)
	}
}

func TestPriorityOrderedWalk(t *testing.T) {
	rows := []poolKeyRow{
		row("low", 100, i64p(30), keys.StatusActive),
		row("high", 100, i64p(10), keys.StatusActive),
		row("mid", 100, i64p(20), keys.StatusActive),
	}
	ps := buildPoolState("hunter:default", "priority", "", rows, bandit.New())
	// Highest priority (lowest number) always wins while available.
	for i := 0; i < 20; i++ {
		k, ok := ps.Select("")
		if !ok || k.id != "high" {
			t.Fatalf("priority pick = %v (ok=%v), want high", k, ok)
		}
	}
	// Take "high" out of rotation -> failover to "mid".
	ps.byID["high"].markStatus(keys.StatusExhausted)
	k, ok := ps.Select("")
	if !ok || k.id != "mid" {
		t.Fatalf("priority failover pick = %v (ok=%v), want mid", k, ok)
	}
}

func TestNoAvailableKey(t *testing.T) {
	rows := []poolKeyRow{
		row("a", 100, nil, keys.StatusDisabled),
		row("b", 100, nil, keys.StatusArchived),
	}
	ps := buildPoolState("hunter:default", "round_robin", "", rows, bandit.New())
	if _, ok := ps.Select(""); ok {
		t.Fatal("Select returned a key when none are available")
	}
}

func TestWeightedSelectRespectsAvailability(t *testing.T) {
	rows := []poolKeyRow{
		row("a", 70, nil, keys.StatusActive),
		row("b", 20, nil, keys.StatusActive),
		row("c", 10, nil, keys.StatusActive),
	}
	ps := buildPoolState("hunter:default", "weighted", "", rows, bandit.New())
	counts := map[string]int{}
	for i := 0; i < 10000; i++ {
		k, ok := ps.Select("")
		if !ok {
			t.Fatal("weighted returned no key")
		}
		counts[k.id]++
	}
	// "a" (weight 70) should dominate.
	if counts["a"] <= counts["b"] || counts["a"] <= counts["c"] {
		t.Fatalf("weighted distribution not weight-proportional: %v", counts)
	}
}

func TestBandedStrategySelects(t *testing.T) {
	rows := []poolKeyRow{
		{ID: "hi", EnvelopeID: "e1", Weight: 100, Status: keys.StatusActive, SuccessEWMA: f64p(0.95)},
		{ID: "lo", EnvelopeID: "e2", Weight: 100, Status: keys.StatusActive, SuccessEWMA: f64p(0.10)},
	}
	ps := buildPoolState("hunter:default", "highest_success", "", rows, bandit.New())
	// The high-success key should sit in a better (lower-index) band and win.
	wins := 0
	for i := 0; i < 100; i++ {
		k, ok := ps.Select("")
		if !ok {
			t.Fatal("banded strategy returned no key")
		}
		if k.id == "hi" {
			wins++
		}
	}
	if wins < 100 {
		t.Fatalf("highest_success picked the low-success key %d/100 times", 100-wins)
	}
}

func TestRegionBasedRouting(t *testing.T) {
	rows := []poolKeyRow{
		{ID: "eu1", EnvelopeID: "e1", Weight: 100, Status: keys.StatusActive, Region: "eu"},
		{ID: "us1", EnvelopeID: "e2", Weight: 100, Status: keys.StatusActive, Region: "us"},
	}
	ps := buildPoolState("hunter:default", "region_based", `{"fallback_region":"us","inner_strategy":"round_robin"}`, rows, bandit.New())
	k, ok := ps.Select("eu")
	if !ok || k.id != "eu1" {
		t.Fatalf("region eu pick = %v (ok=%v), want eu1", k, ok)
	}
	// Unknown region falls back to the fallback region (us).
	k, ok = ps.Select("apac")
	if !ok || k.id != "us1" {
		t.Fatalf("region apac fallback pick = %v (ok=%v), want us1", k, ok)
	}
}

func f64p(v float64) *float64 { return &v }
