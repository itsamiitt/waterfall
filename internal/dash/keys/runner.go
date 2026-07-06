package keys

import (
	"context"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/tenant"
)

// BulkJobRunner is module 3's crash-recovery executor (OI-KEYS-1c auto-resume, doc 15 §T5b). When a
// dashboardd instance dies mid-import its bulk_jobs row is left 'running' with an expiring lease;
// the queues janitor re-queues resumable kinds (status→'queued', claim/lease cleared) instead of
// failing them. This runner then claims queued key_import rows FOR UPDATE SKIP LOCKED under a fresh
// lease and RESUMES execution from the last committed cursor (`succeeded`+`failed`) — rows commit
// independently and a re-attempted row is recognized as a same-batch fingerprint duplicate, so the
// resume neither double-inserts nor double-charges (G2). It mirrors queues.Janitor's Start/Stop so
// the orchestrator can run it alongside the janitor in dashboardd (this package never wires main).
//
// Resume is bounded to the instance that still holds the import's in-memory staged payload: key
// material is never persisted (doc 05 §7.3), so a survivor lacking the payload parks the job
// 'failed' for operator resubmit. Attempts are capped (maxBulkAttempts) so a crash-looping payload
// stops re-queuing and parks terminally.
type BulkJobRunner struct {
	svc        *Service
	interval   time.Duration
	instanceID string
	log        *slog.Logger
	stop       chan struct{}
}

// NewBulkJobRunner builds the resume poller. interval<=0 defaults to 5s. Each runner gets a UNIQUE
// instance id so the ownership-guarded lease (claimed_by) distinguishes concurrent runners — two
// runners never both drive the same re-claimed row.
func (svc *Service) NewBulkJobRunner(interval time.Duration) *BulkJobRunner {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &BulkJobRunner{
		svc: svc, interval: interval, instanceID: "keys-runner-" + newID(),
		log: svc.log, stop: make(chan struct{}),
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
					r.log.Warn("bulk-job runner sweep", "err", err)
				} else if n > 0 {
					r.log.Info("bulk-job runner resumed jobs", "count", n)
				}
			}
		}
	}()
}

// Stop halts the loop.
func (r *BulkJobRunner) Stop() { close(r.stop) }

// RunOnce sweeps every Tenant once, claiming and driving every currently-queued resumable key_import
// job it can lock (SKIP LOCKED skips rows a concurrent runner already holds — no double-claim).
// Returns the number of jobs driven this sweep. Exposed for deterministic tests and for the loop.
func (r *BulkJobRunner) RunOnce(ctx context.Context) (int, error) {
	tenants, err := r.svc.store.listTenants(ctx)
	if err != nil {
		return 0, err
	}
	driven := 0
	for _, tid := range tenants {
		tctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tid})
		for {
			cl, ok, cerr := r.svc.store.claimNextBulkJob(tctx, r.instanceID)
			if cerr != nil {
				r.log.Warn("bulk-job runner claim", "tenant", tid, "err", cerr)
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

// drive resumes one claimed job, or parks it 'failed' when its attempts are exhausted.
func (r *BulkJobRunner) drive(ctx context.Context, cl bulkJobClaim) {
	if cl.Attempts > maxBulkAttempts {
		if owned, _ := r.svc.store.finishBulkJobOwned(ctx, cl.ID, r.instanceID, StatusImportFailed, cl.Succeeded, cl.Failed,
			"", "", marshalJSONString(map[string]any{"reason": "max resume attempts exceeded"})); owned {
			_ = r.svc.store.finishBatch(ctx, cl.ID, StatusImportFailed, "")
		}
		r.log.Warn("bulk-job runner parked (attempts exhausted)", "job", cl.ID, "attempts", cl.Attempts)
		return
	}
	switch cl.Kind {
	case bulkKindKeyImport, bulkKindKeyBulk:
		r.svc.driveClaimedImport(ctx, cl.ID, r.instanceID, cl.Succeeded, cl.Failed)
	default:
		r.log.Warn("bulk-job runner: unhandled kind", "job", cl.ID, "kind", cl.Kind)
	}
}
