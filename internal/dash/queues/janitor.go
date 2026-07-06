package queues

import (
	"context"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// The bulk-jobs janitor closes OI-KEYS-1b/1c: a dashboardd instance that dies mid-run leaves its
// bulk_jobs row 'running' with an expired lease. The claim query only picks up 'queued' rows, so
// without a sweeper the row (and its one-in-flight-per-(tenant,kind,scope) index slot) would be
// wedged forever. For RESUMABLE kinds (key_import / key_bulk / replay — rows commit independently)
// the janitor RE-QUEUES the row (status→'queued', claim/lease cleared) so a survivor's BulkJobRunner
// re-claims and resumes from the last committed cursor (OI-KEYS-1c auto-resume). Non-resumable kinds,
// a cancel that outraced its executor, or a resumable job whose attempts are exhausted are parked
// terminally ('partial' when some rows committed, else 'failed'; 'cancelled' on a pending cancel) —
// the "park for humans" doctrine. Either transition releases the one-in-flight index.
//
// bulk_jobs is Class T (tenant-scoped RLS), so the sweep enumerates Tenants under the platform
// system Principal (like the session/mfa reapers) and reclaims per-Tenant under that Tenant's GUC.

const janitorAdvisoryLock = int64(0x6b65796a616e) // "keyjan" — one leader sweeps across instances

// maxBulkAttempts caps re-queue retries before a resumable job is parked terminally, so a
// crash-looping payload cannot wedge the one-in-flight index indefinitely (doc 15 §T5b).
const maxBulkAttempts = 5

// resumableKindsSQL is the SQL list literal of kinds the janitor re-queues instead of failing.
const resumableKindsSQL = `'key_import','key_bulk','replay'`

// ReclaimExpired sweeps every expired-lease running bulk_job across all Tenants: resumable kinds go
// back to 'queued' for a runner to resume; everything else is parked terminally. Returns the count
// swept. Safe to call from any instance; the caller gates on the advisory lock for a single sweep.
func (s *Service) ReclaimExpired(ctx context.Context) (int, error) {
	sysCtx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: "platform", Scopes: []string{"role:operator"}})
	var tenants []string
	if err := s.store.Tx(sysCtx, func(c *pg.Conn) error {
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
	total := 0
	for _, tid := range tenants {
		tctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tid})
		_ = s.store.Tx(tctx, func(c *pg.Conn) error {
			// A single CASE'd UPDATE so the whole sweep for a Tenant is one statement. `resume` is the
			// re-queue predicate: a resumable kind, not cancelled, with attempts left.
			res, err := c.QueryParams(`update bulk_jobs set
				status = case
				  when cancel_requested then 'cancelled'
				  when kind in (`+resumableKindsSQL+`) and attempts < $1 then 'queued'
				  when succeeded + failed > 0 then 'partial'
				  else 'failed' end,
				claimed_by = case
				  when not cancel_requested and kind in (`+resumableKindsSQL+`) and attempts < $1 then null
				  else claimed_by end,
				lease_expires_at = null,
				finished_at = case
				  when not cancel_requested and kind in (`+resumableKindsSQL+`) and attempts < $1 then finished_at
				  else now() end,
				error_summary = case
				  when cancel_requested then jsonb_build_object('reason','cancelled: reclaimed after cancel request')
				  when kind in (`+resumableKindsSQL+`) and attempts < $1 then error_summary
				  else jsonb_build_object('reason','reclaimed: executing instance lease expired; resubmit','attempts',attempts) end
				where status='running' and lease_expires_at is not null and lease_expires_at < now()
				returning id`, int64(maxBulkAttempts))
			if err != nil {
				return err
			}
			total += len(res.Rows)
			return nil
		})
	}
	return total, nil
}

// Janitor runs ReclaimExpired on an interval under an advisory lock so exactly one instance sweeps.
type Janitor struct {
	svc      *Service
	interval time.Duration
	log      *slog.Logger
	stop     chan struct{}
}

// NewJanitor builds the sweeper. interval<=0 defaults to 30s.
func (s *Service) NewJanitor(interval time.Duration) *Janitor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Janitor{svc: s, interval: interval, log: s.log, stop: make(chan struct{})}
}

// Start runs the sweep loop until Stop. ctx should carry no principal (the sweep binds its own).
func (j *Janitor) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(j.interval)
		defer t.Stop()
		for {
			select {
			case <-j.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if !j.tryLead(ctx) {
					continue // another instance holds the sweep lock this tick
				}
				n, err := j.svc.ReclaimExpired(ctx)
				if err != nil {
					j.log.Warn("bulk-job janitor sweep", "err", err)
				} else if n > 0 {
					j.log.Info("bulk-job janitor reclaimed expired-lease jobs", "count", n)
				}
			}
		}
	}()
}

// Stop halts the loop.
func (j *Janitor) Stop() { close(j.stop) }

// tryLead takes the transient advisory lock for one sweep, releasing it before returning. A single
// instance thus sweeps per tick even across N replicas (best-effort; the reclaim UPDATE is itself
// idempotent, so an overlapping sweep is harmless).
func (j *Janitor) tryLead(ctx context.Context) bool {
	sysCtx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: "platform", Scopes: []string{"role:operator"}})
	got := false
	_ = j.svc.store.Tx(sysCtx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select pg_try_advisory_xact_lock($1)`, janitorAdvisoryLock)
		if err != nil {
			return err
		}
		if len(res.Rows) == 1 && res.Rows[0][0] != nil && *res.Rows[0][0] == "t" {
			got = true
		}
		return nil
	})
	return got
}
