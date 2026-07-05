package heartbeat

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeTransport is an in-memory stand-in for the dashboard heartbeat endpoint: it echoes a
// (mutable, operator-settable) desired_state and records the last reported status.
type fakeTransport struct {
	mu         sync.Mutex
	desired    string
	lastStatus string
	statuses   []string
	beats      int
}

func (f *fakeTransport) Send(_ context.Context, b Beat) (Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastStatus = b.Status
	f.statuses = append(f.statuses, b.Status)
	f.beats++
	return Ack{DesiredState: f.desired}, nil
}

func (f *fakeTransport) setDesired(d string) {
	f.mu.Lock()
	f.desired = d
	f.mu.Unlock()
}

func (f *fakeTransport) status() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastStatus
}

func (f *fakeTransport) sawStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.statuses {
		if s == Stopped {
			return true
		}
	}
	return false
}

// TestClient_DrainConverges is P5 acceptance #2 (client side): a worker with 3 in-flight jobs
// receives desired_state=draining; claims stop IMMEDIATELY; it reports stopped ONLY once
// jobs_active reaches 0; no job is abandoned.
func TestClient_DrainConverges(t *testing.T) {
	ft := &fakeTransport{desired: Running}
	c := New(Config{Transport: ft, WorkerID: "w-enrich-7"})
	ctx := context.Background()
	c.SetJobsActive(3)

	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("beat: %v", err)
	}
	if !c.ShouldClaim() {
		t.Fatal("a running worker must claim")
	}
	if ft.status() != Running {
		t.Fatalf("want running, got %q", ft.status())
	}

	// Operator drains. The worker learns desired_state=draining from THIS beat's ack (the
	// heartbeat is the only control channel), so claiming stops immediately even though the
	// converged status is reported from the next beat onward.
	ft.setDesired(Draining)
	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("beat: %v", err)
	}
	if c.ShouldClaim() {
		t.Fatal("claims must stop IMMEDIATELY once draining is echoed")
	}
	if c.Done() {
		t.Fatal("must not be done while jobs are in flight")
	}

	// Finish the 3 in-flight jobs, beating between; the worker reports draining while jobs remain
	// and NEVER stopped until jobs_active reaches 0 (no job abandoned).
	for i := 3; i > 0; i-- {
		if _, err := c.Beat(ctx); err != nil {
			t.Fatalf("beat: %v", err)
		}
		if ft.status() != Draining {
			t.Fatalf("with %d jobs in flight the worker must report draining, got %q", i, ft.status())
		}
		if ft.sawStopped() {
			t.Fatalf("reported stopped with %d jobs still in flight — a job was abandoned", i)
		}
		c.FinishJob()
	}
	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("beat: %v", err)
	}
	if ft.status() != Stopped {
		t.Fatalf("after jobs_active reached 0 the worker must report stopped, got %q", ft.status())
	}
	if !c.Done() {
		t.Fatal("worker must be Done once drained")
	}
	t.Log("PASS acceptance #2 (client): drain stops claiming immediately; converges to stopped only at jobs_active=0; no job abandoned")
}

// TestClient_DrainRace exercises the beat loop concurrently with worker goroutines reporting job
// completions (run under -race). The Run loop must converge to Done once all jobs finish.
func TestClient_DrainRace(t *testing.T) {
	ft := &fakeTransport{desired: Draining}
	c := New(Config{Transport: ft, WorkerID: "w-race"})
	c.SetJobsActive(8)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, time.Millisecond) }()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(2 * time.Millisecond)
			c.FinishJob()
		}()
	}
	wg.Wait()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-ctx.Done():
		t.Fatal("drain did not converge before timeout")
	}
	if !c.Done() {
		t.Fatal("client must be Done after draining all jobs")
	}
}

// TestClient_PauseKeepsProcess verifies paused stops claiming but never converges to stopped.
func TestClient_PauseKeepsProcess(t *testing.T) {
	ft := &fakeTransport{desired: Paused}
	c := New(Config{Transport: ft, WorkerID: "w-pause"})
	ctx := context.Background()
	c.SetJobsActive(1)
	// Beat 1 learns desired=paused from the ack; beat 2 reports the converged paused status.
	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("beat: %v", err)
	}
	if c.ShouldClaim() {
		t.Fatal("paused worker must not claim")
	}
	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("beat: %v", err)
	}
	if ft.status() != Paused {
		t.Fatalf("want paused, got %q", ft.status())
	}
	if c.Done() {
		t.Fatal("paused worker keeps its process up (never Done)")
	}
}
