package job

import "testing"

func TestQueue_ShedsWhenLaneFull(t *testing.T) {
	q := NewQueue(1)
	if !q.Submit(&Job{Priority: PriorityPremium}) {
		t.Fatal("first premium submit should succeed")
	}
	if q.Submit(&Job{Priority: PriorityPremium}) {
		t.Fatal("second premium submit should be shed (lane full)")
	}
	// The bulk lane is independent, so a bulk job still fits.
	if !q.Submit(&Job{Priority: PriorityBulk}) {
		t.Fatal("bulk lane should be independent of premium lane")
	}
}

func TestQueue_PrefersPremium(t *testing.T) {
	q := NewQueue(4)
	bulk := &Job{ID: "bulk", Priority: PriorityBulk}
	prem := &Job{ID: "prem", Priority: PriorityPremium}
	// Enqueue bulk first, then premium.
	q.Submit(bulk)
	q.Submit(prem)

	done := make(chan struct{})
	got, ok := q.dequeue(done)
	if !ok || got.ID != "prem" {
		t.Fatalf("dequeue should prefer the premium job, got %+v", got)
	}
	got2, ok := q.dequeue(done)
	if !ok || got2.ID != "bulk" {
		t.Fatalf("second dequeue should be the bulk job, got %+v", got2)
	}
}

func TestQueue_DequeueUnblocksOnDone(t *testing.T) {
	q := NewQueue(1)
	done := make(chan struct{})
	close(done)
	if _, ok := q.dequeue(done); ok {
		t.Fatal("dequeue on a closed done with no jobs should return ok=false")
	}
}
