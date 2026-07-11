package research

import (
	"context"
	"log/slog"
	"sync"

	"github.com/enrichment/waterfall/internal/tenant"
)

// runStore is the persistence the Runner needs: transition a run + persist the assembled Dossier.
// *Store satisfies it.
type runStore interface {
	SetRunStatus(ctx context.Context, runID, status string) error
	SaveDossier(ctx context.Context, dossierID, subjectKey string, d Dossier) error
}

// Runner processes queued research runs asynchronously, in memory (the first async increment; a durable
// relay via pgoutbox is a later refinement). A submission enqueues a task carrying the tenant Principal;
// a worker rebuilds a background context bound to that Principal (so RLS still confines every write to the
// submitting tenant — no cross-tenant scan, no BYPASSRLS), transitions the run queued→running, assembles
// the Dossier via the SAME orchestrator seam as the sync path, persists it, and marks the run done|failed.
type Runner struct {
	assembler Assembler
	store     runStore
	log       *slog.Logger
	queue     chan runTask
	quit      chan struct{}
	wg        sync.WaitGroup
}

// runTask carries the Principal (not a request context — a background job outlives the request) plus the
// run id and the subject to assemble.
type runTask struct {
	principal tenant.Principal
	runID     string
	subject   Subject
}

// NewRunner builds an async run processor with a bounded in-memory queue. A queueSize <= 0 defaults to 64.
func NewRunner(assembler Assembler, store runStore, queueSize int, logger *slog.Logger) *Runner {
	if queueSize <= 0 {
		queueSize = 64
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		assembler: assembler,
		store:     store,
		log:       logger,
		queue:     make(chan runTask, queueSize),
		quit:      make(chan struct{}),
	}
}

// Start launches n worker goroutines (n <= 0 defaults to 1). Call Stop to drain-and-exit.
func (r *Runner) Start(n int) {
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			for {
				select {
				case <-r.quit:
					return
				case t := <-r.queue:
					r.process(t)
				}
			}
		}()
	}
}

// Stop signals the workers to exit and waits for the in-flight task on each to finish. Tasks still queued
// are dropped; their runs remain persisted as queued/running and are recoverable by a future poller.
func (r *Runner) Stop() {
	close(r.quit)
	r.wg.Wait()
}

// Submit enqueues a queued run for async assembly, capturing the tenant Principal from the request context
// (never the request context itself). Returns false when the caller has no Principal, or when the queue is
// full (backpressure) — the run stays queued for a later retry.
func (r *Runner) Submit(reqCtx context.Context, runID string, subject Subject) bool {
	p, err := tenant.FromContext(reqCtx)
	if err != nil {
		return false
	}
	select {
	case r.queue <- runTask{principal: p, runID: runID, subject: subject}:
		return true
	default:
		return false
	}
}

// process transitions a run through its lifecycle: running → (assemble → persist) → done, or → failed on
// any error. It runs under a fresh background context bound to the submitting Principal (G1 via RLS).
func (r *Runner) process(t runTask) {
	ctx := tenant.WithPrincipal(context.Background(), t.principal)
	_ = r.store.SetRunStatus(ctx, t.runID, RunRunning)

	dossier, err := r.assembler.Assemble(ctx, t.subject)
	if err != nil {
		r.log.Warn("research run assembly failed", "run_id", t.runID, "err", err)
		_ = r.store.SetRunStatus(ctx, t.runID, RunFailed)
		return
	}
	dossier.DossierID = subjectID(t.subject)
	if err := r.store.SaveDossier(ctx, dossier.DossierID, subjectID(t.subject), dossier); err != nil {
		r.log.Warn("research run persist failed", "run_id", t.runID, "err", err)
		_ = r.store.SetRunStatus(ctx, t.runID, RunFailed)
		return
	}
	_ = r.store.SetRunStatus(ctx, t.runID, RunDone)
}
