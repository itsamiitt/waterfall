package durable

import (
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/job"
)

// Relay moves durable outbox intents onto the in-process Queue for the worker pool to
// execute. It is the "message relay" half of the transactional-outbox pattern (docs/10
// §4). It publishes each still-pending job at most once per process lifetime (an
// in-memory inflight guard), while recovery after a crash re-drives every job that is
// not yet durably terminal (at-least-once execution; G2 dedupes).
type Relay struct {
	store *Store
	queue *job.Queue

	mu       sync.Mutex
	inflight map[string]struct{}

	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// NewRelay builds a relay over a durable store and an in-process queue.
func NewRelay(store *Store, queue *job.Queue) *Relay {
	return &Relay{store: store, queue: queue, inflight: map[string]struct{}{}, done: make(chan struct{})}
}

// DrainOnce publishes all currently-pending outbox jobs that are not already inflight,
// returning how many were newly enqueued. It prunes the inflight guard down to the still
// pending set first, so completed jobs stop occupying it.
func (r *Relay) DrainOnce() int {
	pending := r.store.PendingOutbox()
	pend := make(map[string]struct{}, len(pending))
	for _, j := range pending {
		pend[j.ID] = struct{}{}
	}

	r.mu.Lock()
	for id := range r.inflight {
		if _, ok := pend[id]; !ok {
			delete(r.inflight, id) // no longer pending (became terminal) — release the guard
		}
	}
	published := 0
	for _, j := range pending {
		if _, busy := r.inflight[j.ID]; busy {
			continue
		}
		if r.queue.Submit(j) {
			r.inflight[j.ID] = struct{}{}
			published++
		}
		// If Submit fails (queue full) we leave it pending and retry on the next drain.
	}
	r.mu.Unlock()
	return published
}

// Start runs DrainOnce on an interval until Stop. It also drains immediately so recovered
// jobs are re-driven at startup.
func (r *Relay) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	r.DrainOnce()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-r.done:
				return
			case <-t.C:
				r.DrainOnce()
			}
		}
	}()
}

// Stop halts the background drain loop.
func (r *Relay) Stop() {
	r.once.Do(func() { close(r.done) })
	r.wg.Wait()
}
