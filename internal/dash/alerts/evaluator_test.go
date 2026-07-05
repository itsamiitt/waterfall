package alerts

import (
	"testing"
	"time"
)

// TestDecideNotify_OverrunOncePerCooldown is P6 gate #1 (evaluator unit test, injectable clock): a
// breach sustained across 5 cycles with cooldown 120s produces exactly 1 fire, then 1 renotify once
// the cooldown elapses; a subsequent recovery resolves once after 3 clean cycles.
func TestDecideNotify_OverrunOncePerCooldown(t *testing.T) {
	cooldown := 120 * time.Second
	base := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	var ep episode
	var fired, renotify, resolved int

	// 5 breaching cycles at 30s cadence: t = 0,30,60,90,120.
	for i := 0; i < 5; i++ {
		act, next := decideNotify(ep, true, base.Add(time.Duration(i)*30*time.Second), cooldown)
		ep = next
		tally(act, &fired, &renotify, &resolved)
	}
	if fired != 1 {
		t.Fatalf("fired=%d, want exactly 1 across the sustained breach", fired)
	}
	if renotify != 0 {
		t.Fatalf("renotify=%d before cooldown elapsed, want 0", renotify)
	}

	// One more breaching cycle AFTER the cooldown has clearly elapsed (t=150 > notifiedAt+120).
	act, next := decideNotify(ep, true, base.Add(150*time.Second), cooldown)
	ep = next
	tally(act, &fired, &renotify, &resolved)
	if renotify != 1 {
		t.Fatalf("renotify=%d after cooldown, want exactly 1", renotify)
	}
	if fired != 1 {
		t.Fatalf("fired=%d, must not increment on renotify", fired)
	}

	// Recovery: 3 consecutive clean evaluations resolve exactly once.
	now := base.Add(180 * time.Second)
	for i := 0; i < 3; i++ {
		act, next := decideNotify(ep, false, now, cooldown)
		ep = next
		tally(act, &fired, &renotify, &resolved)
		now = now.Add(30 * time.Second)
	}
	if resolved != 1 {
		t.Fatalf("resolved=%d, want exactly 1 after 3 clean cycles", resolved)
	}
	if ep.open {
		t.Fatalf("episode should be closed after resolve")
	}
	// A 4th clean cycle notifies nothing.
	act, _ = decideNotify(ep, false, now, cooldown)
	if act != actNone {
		t.Fatalf("closed episode should not notify, got %v", act)
	}
}

// TestDecideNotify_ResolveHysteresis proves a single clean blip does NOT resolve a firing episode
// (N-of-M / 3-clean hysteresis, doc 10 §5.1).
func TestDecideNotify_ResolveHysteresis(t *testing.T) {
	cooldown := 120 * time.Second
	now := time.Now()
	var ep episode
	// fire
	_, ep = decideNotify(ep, true, now, cooldown)
	// two clean cycles, then a breach resets the clean streak
	_, ep = decideNotify(ep, false, now, cooldown)
	_, ep = decideNotify(ep, false, now, cooldown)
	if !ep.open {
		t.Fatalf("episode resolved too early (only 2 clean cycles)")
	}
	act, next := decideNotify(ep, true, now.Add(10*time.Second), cooldown)
	ep = next
	if act != actNone {
		t.Fatalf("a breach within cooldown after clean cycles should not renotify, got %v", act)
	}
	if ep.cleanStreak != 0 {
		t.Fatalf("clean streak should reset on a breach, got %d", ep.cleanStreak)
	}
}

// TestDecideNotify_AckSuppressesRenotifyNotResolve is P6 gate #1 (ack semantics): an acked episode
// does NOT renotify after cooldown, but resolve still notifies.
func TestDecideNotify_AckSuppressesRenotifyNotResolve(t *testing.T) {
	cooldown := 120 * time.Second
	base := time.Now()
	var ep episode
	act, next := decideNotify(ep, true, base, cooldown)
	ep = next
	if act != actFire {
		t.Fatalf("first breach should fire, got %v", act)
	}
	ep.acked = true // operator acknowledged

	// Breach well past cooldown: renotify is suppressed by the ack.
	act, next = decideNotify(ep, true, base.Add(300*time.Second), cooldown)
	ep = next
	if act != actNone {
		t.Fatalf("acked episode must not renotify, got %v", act)
	}

	// Recovery still resolves (and notifies) despite the ack.
	var resolved int
	now := base.Add(330 * time.Second)
	for i := 0; i < 3; i++ {
		act, ep = decideNotify(ep, false, now, cooldown)
		if act == actResolve {
			resolved++
		}
		now = now.Add(30 * time.Second)
	}
	if resolved != 1 {
		t.Fatalf("resolve must notify even after ack; resolved=%d", resolved)
	}
}

// TestDedupeKeysDeterministic pins the correlation/dedupe key derivation.
func TestDedupeKeysDeterministic(t *testing.T) {
	scope := map[string]string{"provider_id": "hunter", "workflow_key": "email"}
	a := eventDedupeKey("acme", "rule-1", scope)
	b := eventDedupeKey("acme", "rule-1", map[string]string{"workflow_key": "email", "provider_id": "hunter"})
	if a != b {
		t.Fatalf("event dedupe key must be order-independent: %s != %s", a, b)
	}
	if eventDedupeKey("globex", "rule-1", scope) == a {
		t.Fatalf("event dedupe key must vary by tenant")
	}
	n1 := notifDedupeKey(a, "chan-1", occFired)
	if n1 != notifDedupeKey(a, "chan-1", occFired) {
		t.Fatalf("notification dedupe key must be deterministic")
	}
	if n1 == notifDedupeKey(a, "chan-1", occResolved) {
		t.Fatalf("fired and resolved occasions must produce distinct dedupe keys")
	}
	if occRenotify(0) == occRenotify(1) {
		t.Fatalf("distinct cooldown buckets must produce distinct renotify occasions")
	}
}

// TestValidateRule covers the closed-vocabulary + scope + op gates.
func TestValidateRule(t *testing.T) {
	ok := Rule{Metric: "provider.error_rate", Op: "gt", Threshold: 0.05, Scope: map[string]string{"provider_id": "h"}}
	if err := validateRule(ok); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	if err := validateRule(Rule{Metric: "made.up", Op: "gt"}); err == nil {
		t.Fatalf("unknown metric must be rejected")
	}
	if err := validateRule(Rule{Metric: "provider.error_rate", Op: "??"}); err == nil {
		t.Fatalf("invalid op must be rejected")
	}
	if err := validateRule(Rule{Metric: "provider.error_rate", Op: "gt", Scope: map[string]string{"queue": "x"}}); err == nil {
		t.Fatalf("scope key outside the metric's allowed set must be rejected")
	}
}

func tally(act evalAction, fired, renotify, resolved *int) {
	switch act {
	case actFire:
		*fired++
	case actRenotify:
		*renotify++
	case actResolve:
		*resolved++
	}
}
