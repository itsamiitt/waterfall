package queues

import (
	"context"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// The bulk-jobs janitor closes OI-KEYS-1b: a dashboardd instance that dies mid-run leaves its
// bulk_jobs row in 'running' with an expired lease. The claim query only picks up 'queued' rows, so
// without a sweeper the row (and its one-in-flight-per-(tenant,kind,scope) index slot) would be
// wedged forever. The janitor transitions expired-lease running rows to a terminal 'failed' with a
// clear reason, releasing the index so the operator can resubmit — the "park for humans" doctrine
// used across the dashboard (never silently resume a partially-applied bulk mutation). Automatic
// re-execution/resume of a crashed bulk job is the documented residual OI-KEYS-1c.
//
// bulk_jobs is Class T (tenant-scoped RLS), so the sweep enumerates Tenants under the platform
// system Principal (like the session/mfa reapers) and reclaims per-Tenant under that Tenant's GUC.

const janitorAdvisoryLock = int64(0x6b65796a616e) // "keyjan" — one leader sweeps across instances

// ReclaimExpired fails every expired-lease running bulk_job across all Tenants and returns the count
// reclaimed. Safe to call from any instance; the caller gates on the advisory lock for single-sweep.
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
			res, err := c.QueryParams(`update bulk_jobs
				set status='failed', finished_at=now(), lease_expires_at=null,
				    error_summary=jsonb_build_object('reason','reclaimed: executing instance lease expired; resubmit')
				where status='running' and lease_expires_at is not null and lease_expires_at < now()
				returning id`)
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
