package health

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestScheduler_BoundedConcurrencyAndWrites drives runOnce with a fake blocking check function and
// proves (a) every enabled target is probed exactly once, (b) at most Concurrency probes run at a
// time (worker-pool bound), and (c) each probe writes a check row. Run under -race.
func TestScheduler_BoundedConcurrencyAndWrites(t *testing.T) {
	const n, limit = 24, 4

	var inflight int32
	var mu sync.Mutex
	maxInflight := int32(0)

	check := func(_ context.Context, _ Target) CheckResult {
		cur := atomic.AddInt32(&inflight, 1)
		mu.Lock()
		if cur > maxInflight {
			maxInflight = cur
		}
		mu.Unlock()
		time.Sleep(3 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		return CheckResult{Status: StatusUp, LatencyMS: 5}
	}

	fake := newFakeStore()
	for i := 0; i < n; i++ {
		fake.targets = append(fake.targets, Target{
			ProviderID: fmt.Sprintf("p%02d", i), BaseURL: "http://x",
			IntervalS: 60, JitterPct: 10, Enabled: true,
		})
	}

	fixed := time.Unix(1_700_000_000, 0).UTC()
	sch := NewScheduler(Deps{store: fake, Check: check, Concurrency: limit, Now: func() time.Time { return fixed }})

	sch.runOnce(context.Background(), fake.targets)

	if got := fake.writeCount(); got != n {
		t.Fatalf("writes=%d, want %d (one per target)", got, n)
	}
	mu.Lock()
	peak := maxInflight
	mu.Unlock()
	if peak > limit {
		t.Fatalf("peak in-flight=%d exceeds concurrency limit %d", peak, limit)
	}
	if peak < 2 {
		t.Fatalf("expected the worker pool to overlap probes; peak=%d", peak)
	}
}

// TestScheduler_RunOnceSkipsDisabledAndNotDue proves disabled targets are skipped and a target is
// not re-run before its next-due time.
func TestScheduler_RunOnceSkipsDisabledAndNotDue(t *testing.T) {
	fake := newFakeStore()
	targets := []Target{
		{ProviderID: "on", BaseURL: "http://x", IntervalS: 60, Enabled: true},
		{ProviderID: "off", BaseURL: "http://x", IntervalS: 60, Enabled: false},
	}
	fixed := time.Unix(1_700_000_000, 0).UTC()
	sch := NewScheduler(Deps{
		store: fake, Concurrency: 2, Now: func() time.Time { return fixed },
		Check: func(context.Context, Target) CheckResult { return CheckResult{Status: StatusUp} },
	})

	sch.runOnce(context.Background(), targets)
	if got := fake.writeCount(); got != 1 {
		t.Fatalf("first pass writes=%d, want 1 (disabled skipped)", got)
	}
	// Immediate second pass at the same clock: "on" is not due yet (next-due is fixed+~60s).
	sch.runOnce(context.Background(), targets)
	if got := fake.writeCount(); got != 1 {
		t.Fatalf("second pass writes=%d, want 1 (not due)", got)
	}
}

func TestScheduler_JitterWithinBounds(t *testing.T) {
	sch := NewScheduler(Deps{
		store: newFakeStore(),
		Check: func(context.Context, Target) CheckResult { return CheckResult{} },
		Now:   func() time.Time { return time.Unix(1, 0) },
	})

	tg := Target{IntervalS: 100, JitterPct: 20}
	lo, hi := 80*time.Second, 120*time.Second
	seen := map[time.Duration]bool{}
	for i := 0; i < 300; i++ {
		d := sch.jittered(tg)
		if d < lo || d > hi {
			t.Fatalf("jittered=%v outside [%v,%v]", d, lo, hi)
		}
		seen[d] = true
	}
	if len(seen) < 10 {
		t.Fatalf("jitter not varying: %d distinct values", len(seen))
	}
	// Zero jitter is exact.
	if d := sch.jittered(Target{IntervalS: 60, JitterPct: 0}); d != 60*time.Second {
		t.Fatalf("zero-jitter interval=%v, want 60s", d)
	}
}

// TestReactivator_ProbesExhaustedKeys proves the reactivator asks the injected KeyReactivator to
// probe each candidate key and counts recoveries vs failures — the single rotation touch-point,
// exercised purely through the interface.
func TestReactivator_ProbesExhaustedKeys(t *testing.T) {
	fake := newFakeStore()
	fake.exhausted = []string{"k1", "k2", "k3"}
	fr := &fakeReactivator{failKeys: map[string]bool{"k2": true}}

	r := NewReactivator(Deps{store: fake, Reactivator: fr})
	if r == nil {
		t.Fatal("NewReactivator returned nil with a reactivator wired")
	}
	recovered, attempted, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if attempted != 3 || recovered != 2 {
		t.Fatalf("attempted=%d recovered=%d, want 3/2", attempted, recovered)
	}
	if len(fr.probed) != 3 {
		t.Fatalf("probed %d keys, want 3", len(fr.probed))
	}
}

func TestReactivator_NilWhenUnwired(t *testing.T) {
	if r := NewReactivator(Deps{store: newFakeStore()}); r != nil {
		t.Fatal("NewReactivator should be nil when no KeyReactivator is injected")
	}
}
