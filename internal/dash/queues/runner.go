package queues

import (
	"context"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// BulkJobRunner is the queues feature's crash-recovery executor (OI-KEYS-1c auto-resume, doc 15
// §T5b). When the janitor re-queues an expired-lease replay (status→'queued'), this runner claims it
// FOR UPDATE SKIP LOCKED under a fresh lease and RESUMES the redrive from the persisted cursor —
// redriven rows have already left the dead set, so a re-scan finds only the remainder and each dead
// job is redriven exactly once across the original attempt and the resume (idempotent; no double
// charge). It mirrors queues.Janitor's Start/Stop so the orchestrator runs it alongside the janitor
// (and the keys.BulkJobRunner) in dashboardd. A unique instance id makes the ownership-guarded lease
// distinguish concurrent runners — two runners never both drive the same re-claimed row.
type BulkJobRunner struct {
	svc        *Service
	interval   time.Duration
	instanceID string
	log        *slog.Logger
	stop       chan struct{}
}

// NewBulkJobRunner builds the replay resume poller. interval<=0 defaults to 5s.
func (s *Service) NewBulkJobRunner(interval time.Duration) *BulkJobRunner {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &BulkJobRunner{
		svc: s, interval: interval, instanceID: "queues-runner-" + shortID(),
		log: s.log, stop: make(chan struct{}),
	}
}

// Start runs the resume loop until Stop. ctx should carry no principal (each sweep binds its own).
func (r *BulkJobRunner) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(r.interval)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := r.RunOnce(ctx); err != nil {
					r.log.Warn("replay runner sweep", "err", err)
				} else if n > 0 {
					r.log.Info("replay runner resumed jobs", "count", n)
				}
			}
		}
	}()
}

// Stop halts the loop.
func (r *BulkJobRunner) Stop() { close(r.stop) }

// RunOnce sweeps every Tenant once, claiming and driving every queued replay it can lock (SKIP
// LOCKED skips rows a concurrent runner holds — no double-claim). Returns the count driven.
func (r *BulkJobRunner) RunOnce(ctx context.Context) (int, error) {
	sysCtx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: "platform", Scopes: []string{"role:operator"}})
	var tenants []string
	if err := r.svc.store.Tx(sysCtx, func(c *pg.Conn) error {
		res, err := c.Query(`select id from tenants`)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			if row[0] != nil {
				tenants = append(tenants, *row[0])
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	driven := 0
	for _, tid := range tenants {
		tctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tid})
		for {
			cl, ok, cerr := r.svc.replay.claimNext(tctx, r.instanceID)
			if cerr != nil {
				r.log.Warn("replay runner claim", "tenant", tid, "err", cerr)
				break
			}
			if !ok {
				break
			}
			r.drive(tctx, cl)
			driven++
		}
	}
	return driven, nil
}

// drive resumes one claimed replay, or parks it terminally when its attempts are exhausted.
func (r *BulkJobRunner) drive(ctx context.Context, cl replayClaim) {
	if cl.Attempts > maxBulkAttempts {
		status := "failed"
		if cl.Succeeded+cl.Failed > 0 {
			status = "partial"
		}
		if done, _ := r.svc.replay.finish(ctx, cl.ID, r.instanceID, status, cl.Matched, cl.Succeeded, cl.Failed, nil); done {
			r.svc.replay.filters.evict(cl.ID)
		}
		r.log.Warn("replay runner parked (attempts exhausted)", "job", cl.ID, "attempts", cl.Attempts)
		return
	}
	r.svc.replay.resumeClaimed(ctx, r.instanceID, cl)
}
