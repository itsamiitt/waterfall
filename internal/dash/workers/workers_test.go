package workers

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeLostStore is an in-memory LostStore for the detector tests (no Postgres).
type fakeLostStore struct {
	mu     sync.Mutex
	beats  map[string]time.Time
	status map[string]string
	marked map[string]bool
}

func newFakeLostStore() *fakeLostStore {
	return &fakeLostStore{beats: map[string]time.Time{}, status: map[string]string{}, marked: map[string]bool{}}
}

func (f *fakeLostStore) heartbeat(id string, at time.Time) {
	f.mu.Lock()
	f.beats[id] = at
	f.status[id] = StatusRunning
	f.marked[id] = false
	f.mu.Unlock()
}

func (f *fakeLostStore) OverdueWorkers(_ context.Context, cutoff time.Time) ([]OverdueWorker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []OverdueWorker
	for id, beat := range f.beats {
		st := f.status[id]
		if st == StatusLost || st == StatusStopped {
			continue
		}
		if beat.Before(cutoff) {
			out = append(out, OverdueWorker{ID: id, LastBeat: beat})
		}
	}
	return out, nil
}

func (f *fakeLostStore) MarkLost(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status[id] == StatusLost {
		return false, nil
	}
	f.status[id] = StatusLost
	f.marked[id] = true
	return true, nil
}

func (f *fakeLostStore) statusOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[id]
}

// TestDetector_LostAfterThreshold is P5 acceptance #3: heartbeats stop → lost after the 3-interval
// staleness threshold (confirmed by the 2-pass hysteresis); resumed heartbeats restore running.
func TestDetector_LostAfterThreshold(t *testing.T) {
	fs := newFakeLostStore()
	var lostCount int
	var lmu sync.Mutex
	d := NewDetector(DetectorConfig{
		Store: fs, Interval: 10 * time.Second, MissIntervals: 3, Hysteresis: 2,
		OnLost: func(string) { lmu.Lock(); lostCount++; lmu.Unlock() },
	})
	ctx := context.Background()
	t0 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	fs.heartbeat("w1", t0) // last beat at t0

	// Within 3 intervals (age <= 30s): not overdue, never lost.
	must(t, d, ctx, t0.Add(25*time.Second), nil)
	if fs.statusOf("w1") == StatusLost {
		t.Fatal("marked lost inside the 3-interval threshold")
	}

	// First pass past the 3-interval staleness: strike 1 (hysteresis withholds lost).
	must(t, d, ctx, t0.Add(31*time.Second), nil)
	if fs.statusOf("w1") == StatusLost {
		t.Fatal("hysteresis: a single overdue pass must NOT mark lost")
	}

	// Second consecutive overdue pass: committed lost + one alert.
	must(t, d, ctx, t0.Add(41*time.Second), []string{"w1"})
	if fs.statusOf("w1") != StatusLost {
		t.Fatal("w1 must be lost after the hysteresis is satisfied past 3 intervals")
	}
	lmu.Lock()
	if lostCount != 1 {
		t.Fatalf("exactly one lost alert expected, got %d", lostCount)
	}
	lmu.Unlock()

	// Resume: a fresh heartbeat re-adopts the row (status back to running); the detector emits no
	// further alert and resets the strike count.
	fs.heartbeat("w1", t0.Add(45*time.Second))
	must(t, d, ctx, t0.Add(50*time.Second), nil)
	if fs.statusOf("w1") != StatusRunning {
		t.Fatalf("resumed worker must be running, got %q", fs.statusOf("w1"))
	}
	lmu.Lock()
	if lostCount != 1 {
		t.Fatalf("resume must not emit another alert, got %d", lostCount)
	}
	lmu.Unlock()
	t.Log("PASS acceptance #3: lost after 3 missed intervals (2-pass hysteresis); resume restores running; one alert")
}

// TestDetector_FlapNoAlert is the F7 flapping guard: a worker overdue for a single pass that then
// resumes within the hysteresis window emits NO alert.
func TestDetector_FlapNoAlert(t *testing.T) {
	fs := newFakeLostStore()
	var lostCount int
	d := NewDetector(DetectorConfig{
		Store: fs, Interval: 10 * time.Second, MissIntervals: 3, Hysteresis: 2,
		OnLost: func(string) { lostCount++ },
	})
	ctx := context.Background()
	t0 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	fs.heartbeat("w2", t0)

	must(t, d, ctx, t0.Add(31*time.Second), nil) // strike 1 (GC pause / jitter)
	fs.heartbeat("w2", t0.Add(32*time.Second))   // recovered
	must(t, d, ctx, t0.Add(42*time.Second), nil) // fresh again -> strike reset, no lost

	if fs.statusOf("w2") == StatusLost {
		t.Fatal("a flapping worker must not be marked lost")
	}
	if lostCount != 0 {
		t.Fatalf("flapping within hysteresis must emit no alert, got %d", lostCount)
	}
	t.Log("PASS: flapping within the hysteresis window emits no alert")
}

// TestDetector_Race runs the loop concurrently with heartbeats (under -race).
func TestDetector_Race(t *testing.T) {
	fs := newFakeLostStore()
	clk := &clock{t: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)}
	d := NewDetector(DetectorConfig{Store: fs, Interval: time.Millisecond, MissIntervals: 3, Hysteresis: 2, Now: clk.now})
	ctx := context.Background()
	d.Start(ctx)
	defer d.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				fs.heartbeat("w", clk.now())
				clk.advance(time.Millisecond)
				time.Sleep(time.Microsecond)
			}
		}(i)
	}
	wg.Wait()
}

func TestPlanWaves(t *testing.T) {
	got := planWaves([]string{"a", "b", "c", "d", "e"}, 2)
	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("planWaves = %v, want %v", got, want)
	}
	if w := planWaves([]string{"a", "b"}, 0); !reflect.DeepEqual(w, [][]string{{"a"}, {"b"}}) {
		t.Fatalf("maxUnavailable<1 must default to 1, got %v", w)
	}
	if w := planWaves(nil, 3); w != nil {
		t.Fatalf("empty set -> nil waves, got %v", w)
	}
}

func TestConverged(t *testing.T) {
	cases := []struct {
		desired, status string
		jobsActive      int
		want            bool
	}{
		{DesiredRunning, StatusRunning, 0, true},
		{DesiredRunning, StatusStarting, 0, false},
		{DesiredPaused, StatusPaused, 2, true},
		{DesiredDraining, StatusStopped, 0, true},
		{DesiredDraining, StatusStopped, 3, false}, // jobs still in flight
		{DesiredDraining, StatusDraining, 3, false},
		{DesiredStopped, StatusStopped, 0, true},
	}
	for _, c := range cases {
		got := converged(WorkerRow{DesiredState: c.desired, Status: c.status, JobsActive: c.jobsActive})
		if got != c.want {
			t.Fatalf("converged(desired=%s,status=%s,active=%d)=%v want %v", c.desired, c.status, c.jobsActive, got, c.want)
		}
	}
}

// --- helpers ---

func must(t *testing.T, d *Detector, ctx context.Context, now time.Time, wantLost []string) {
	t.Helper()
	got, err := d.Pass(ctx, now)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(wantLost) == 0 && len(got) != 0 {
		t.Fatalf("expected no lost at %v, got %v", now, got)
	}
	if len(wantLost) > 0 && !reflect.DeepEqual(got, wantLost) {
		t.Fatalf("lost at %v = %v, want %v", now, got, wantLost)
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
