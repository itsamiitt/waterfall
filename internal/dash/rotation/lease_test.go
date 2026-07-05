package rotation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// memLeaser is an in-memory stand-in for the guarded UPDATE key_budgets ... WHERE day_leased + $2
// <= daily_limit RETURNING — byte-for-byte the same grant semantics, so the concurrency test proves
// the bucket registry never over-leases given a correct leaser (the live PG path is proven by the
// integration test). It is the exact arithmetic of pgStore.LeaseBatch.
type memLeaser struct {
	mu     sync.Mutex
	leased int64
}

func (m *memLeaser) LeaseBatch(_ context.Context, _ string, batch int, dailyLimit int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leased+int64(batch) <= dailyLimit {
		m.leased += int64(batch)
		return batch, nil
	}
	remaining := dailyLimit - m.leased
	if remaining <= 0 {
		return 0, nil
	}
	if int64(batch) < remaining {
		remaining = int64(batch)
	}
	m.leased += remaining
	return int(remaining), nil
}

// TestLeaseNoOverLease_InMemory is the race-safe unit half of P2 acceptance #1 (doc 12): a
// 50-goroutine lease storm against ONE key with daily_limit N must grant EXACTLY N tokens and never
// more (no over-lease). Run under `go test -race`. The live-PG half is the integration test.
func TestLeaseNoOverLease_InMemory(t *testing.T) {
	const (
		limit        = int64(250)
		goroutines   = 50
		perGoroutine = 50 // 2500 attempts >> 250 so demand exhausts the limit
	)
	leaser := &memLeaser{}
	reg := newBucketRegistry(leaser)

	var granted atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if err := reg.draw(context.Background(), "key-1", limit); err == nil {
					granted.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if granted.Load() > limit {
		t.Fatalf("OVER-LEASE: granted %d leases, daily_limit is %d", granted.Load(), limit)
	}
	if leaser.leased > limit {
		t.Fatalf("OVER-LEASE: day_leased reached %d, daily_limit is %d", leaser.leased, limit)
	}
	if granted.Load() != limit {
		t.Fatalf("granted %d leases with demand %d >> limit %d; want exactly %d",
			granted.Load(), goroutines*perGoroutine, limit, limit)
	}
	t.Logf("PASS no over-lease: %d goroutines drew exactly %d leases (day_leased=%d), limit=%d",
		goroutines, granted.Load(), leaser.leased, limit)
}

// TestUnlimitedKeyNeverHitsDB proves a daily_limit <= 0 (unlimited) never consults the leaser.
func TestUnlimitedKeyNeverHitsDB(t *testing.T) {
	leaser := &memLeaser{}
	reg := newBucketRegistry(leaser)
	for i := 0; i < 1000; i++ {
		if err := reg.draw(context.Background(), "key-1", 0); err != nil {
			t.Fatalf("unlimited draw errored: %v", err)
		}
	}
	if leaser.leased != 0 {
		t.Fatalf("unlimited key consulted the leaser (leased=%d)", leaser.leased)
	}
}

// fakeReconcile is a synthetic reconcileStore over in-memory rows.
type fakeReconcile struct {
	totals  map[string]int64
	err     error
	written map[string]int64
	setN    int
}

func (f *fakeReconcile) UsageDayTotals(_ context.Context) (map[string]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.totals, nil
}

func (f *fakeReconcile) SetBudgetDayUsed(_ context.Context, totals map[string]int64) error {
	f.written = totals
	f.setN++
	return nil
}

// TestReconcile exercises the day_used reconcile over synthetic usage_events totals, plus the
// table-absent (pre-P4) no-op guard.
func TestReconcile(t *testing.T) {
	// Ground-truth totals are written to key_budgets.day_used.
	f := &fakeReconcile{totals: map[string]int64{"k1": 5, "k2": 9}}
	n, err := reconcile(context.Background(), f)
	if err != nil || n != 2 {
		t.Fatalf("reconcile = (%d,%v), want (2,nil)", n, err)
	}
	if f.written["k1"] != 5 || f.written["k2"] != 9 {
		t.Fatalf("reconcile wrote %v, want {k1:5,k2:9}", f.written)
	}

	// usage_events absent (pre-P4): no-op, no write.
	f2 := &fakeReconcile{err: ErrUsageEventsAbsent}
	n, err = reconcile(context.Background(), f2)
	if err != nil || n != 0 || f2.setN != 0 {
		t.Fatalf("reconcile(absent) = (%d,%v) setN=%d, want (0,nil) setN=0", n, err, f2.setN)
	}

	// No usage today: no-op.
	f3 := &fakeReconcile{totals: map[string]int64{}}
	n, err = reconcile(context.Background(), f3)
	if err != nil || n != 0 || f3.setN != 0 {
		t.Fatalf("reconcile(empty) = (%d,%v) setN=%d, want (0,nil) setN=0", n, err, f3.setN)
	}
	t.Logf("PASS reconcile: day_used rewritten from synthetic usage_events; absent-table + empty are no-ops")
}
