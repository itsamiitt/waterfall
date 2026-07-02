package job

import (
	"context"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
)

// RunFunc executes one enrichment request and returns its outcome. The wiring supplies a
// closure over the Router + Execution Engine. It runs under ctx, which already carries
// the job's tenant principal (G1).
type RunFunc func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error)

// Dispatcher pulls jobs off the Queue and runs them through a RunFunc, updating the
// Store. It is the async worker pool (docs/10). The same Run path serves the sync API.
type Dispatcher struct {
	queue      *Queue
	store      Store
	run        RunFunc
	now        func() time.Time
	base       context.Context
	onComplete func(context.Context, *Job)

	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// DispatcherOption configures a Dispatcher.
type DispatcherOption func(*Dispatcher)

// WithClock injects a clock for deterministic timestamps.
func WithClock(now func() time.Time) DispatcherOption {
	return func(d *Dispatcher) { d.now = now }
}

// WithOnComplete registers a hook invoked (in the worker goroutine, under the job's
// principal context) after a job reaches a terminal state. It is how webhook delivery
// plugs in without the job package depending on the webhook package. The hook must not
// panic; it runs after the terminal state is already durably recorded, so a hook failure
// never loses the job result.
func WithOnComplete(fn func(context.Context, *Job)) DispatcherOption {
	return func(d *Dispatcher) { d.onComplete = fn }
}

// NewDispatcher builds a dispatcher over queue/store/run.
func NewDispatcher(queue *Queue, store Store, run RunFunc, opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{
		queue: queue,
		store: store,
		run:   run,
		now:   time.Now,
		base:  context.Background(),
		done:  make(chan struct{}),
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Start spawns n worker goroutines. Each blocks on the queue and runs jobs until Stop.
func (d *Dispatcher) Start(n int) {
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		d.wg.Add(1)
		go d.worker()
	}
}

// Stop signals workers to exit after finishing their current job, and waits. Jobs still
// queued (not yet picked up) are not drained in this in-process build — a production
// build's durable log (ADR-0013) makes them survivable.
func (d *Dispatcher) Stop() {
	d.once.Do(func() { close(d.done) })
	d.wg.Wait()
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for {
		j, ok := d.queue.dequeue(d.done)
		if !ok {
			return
		}
		d.Run(j)
	}
}

// Run executes a single job to completion under its own tenant principal, recording each
// state transition in the Store. It is used by both the async workers and the sync API
// path. It never panics out: a RunFunc error marks the job failed rather than crashing
// the worker.
func (d *Dispatcher) Run(j *Job) {
	ctx := j.contextFor(d.base)

	j.Status = StatusRunning
	j.UpdatedAt = d.now()
	_ = d.store.Put(ctx, j)

	out, err := d.safeRun(ctx, j.Req)
	if err != nil {
		j.Status = StatusFailed
		j.Err = err.Error()
	} else {
		o := out
		j.Status = StatusSucceeded
		j.Outcome = &o
	}
	j.UpdatedAt = d.now()
	_ = d.store.Put(ctx, j)

	// Fire the completion hook (e.g. webhook delivery) after the terminal state is durably
	// recorded, so a hook failure cannot lose the result.
	if d.onComplete != nil {
		d.onComplete(ctx, j)
	}
}

// safeRun converts a panic in a RunFunc into an error so one bad job cannot take down a
// worker (defensive; docs/09 bounded execution).
func (d *Dispatcher) safeRun(ctx context.Context, req domain.EnrichmentRequest) (out engine.Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{v: r}
		}
	}()
	return d.run(ctx, req)
}

type panicError struct{ v any }

func (e *panicError) Error() string { return "job runner panicked" }
