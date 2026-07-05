package telemetry

import (
	"context"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// Queue-stats sampler fold (doc 06 §6): a small, separable addition to the leader aggregator
// that samples job_outbox's per-state counts into queue_stats_{1m,1h}. It is a SEPARATE method
// from the usage-derived Fold/Refold (which it never touches), so existing telemetry folds and
// their idempotency proof are unchanged; the orchestrator drives it on its own cadence.
//
// RLS-clean sampling (deviation from doc §6's single BYPASSRLS aggregate, recorded as OI-QW-2b):
// job_outbox carries NO operator cross-tenant SELECT policy (doc 06 §2.4), so instead of a
// privileged reader this fold iterates the customer-Tenant registry and counts each Tenant's
// rows under that Tenant's own RLS transaction (identical to the usage fold's per-Tenant pass),
// then sums. The result is the same aggregate vector with strictly less privilege — no
// BYPASSRLS reader exists. Counts only, never row payloads.
//
// Idempotency: the fold writes the whole per-(queue,bucket) row with last-sample-wins REPLACE
// semantics — gauges (depth/running/…) are the live snapshot, and enq/deq are the cumulative
// within-bucket flow so the last sample of a bucket records its full count. Re-sampling the same
// instant yields byte-identical rows (the acceptance-gate idempotency property).

// queueVisibilitySeconds mirrors pgoutbox's default reclaim window (doc 06 §1.1): a row claimed
// within this window counts as running, otherwise retry.
const queueVisibilitySeconds = 30

// queueSample accumulates the cross-Tenant state vector for one sampling tick.
type queueSample struct {
	waiting, running, retry, failed, dead int64
	enqMin, deqMin, enqHour, deqHour      int64
	oldestAgeS                            int64 // max over Tenants
}

// QueueStatsFold samples job_outbox and folds the current 1m and 1h queue_stats buckets for
// queue. now is the sample time (injectable). All job_outbox rows belong to the single pgoutbox
// outbox, so they attribute to the one logical queue name (the pgstore backend mapping,
// doc 06 §1.3).
func (a *Aggregator) QueueStatsFold(ctx context.Context, queue string, now time.Time) error {
	now = now.UTC()
	minuteStart := bucketStart(now, Res1m)
	hourStart := bucketStart(now, Res1h)
	visCutoff := now.Add(-queueVisibilitySeconds * time.Second)

	tenants, err := listCustomerTenants(ctx, a.store)
	if err != nil {
		return err
	}
	var acc queueSample
	for _, tid := range tenants {
		tctx := principalFor(ctx, tid)
		txErr := a.store.Tx(tctx, func(c *pg.Conn) error {
			res, qerr := c.QueryParams(`select
			  count(*) filter (where pending and not dead and claimed_at is null and attempts = 0),
			  count(*) filter (where pending and not dead and claimed_at is not null and claimed_at >= $3),
			  count(*) filter (where pending and not dead and attempts >= 1 and (claimed_at is null or claimed_at < $3)),
			  count(*) filter (where not pending and not dead and status = 'failed'),
			  count(*) filter (where dead),
			  count(*) filter (where created_at >= $1 and created_at < $2),
			  count(*) filter (where not pending and status in ('succeeded','failed') and updated_at >= $1 and updated_at < $2),
			  count(*) filter (where created_at >= $5 and created_at < $2),
			  count(*) filter (where not pending and status in ('succeeded','failed') and updated_at >= $5 and updated_at < $2),
			  coalesce(extract(epoch from ($4 - min(created_at) filter (where pending and not dead)))::bigint, 0)
			from job_outbox`, minuteStart, now, visCutoff, now, hourStart)
			if qerr != nil {
				return qerr
			}
			if len(res.Rows) == 0 {
				return nil
			}
			r := res.Rows[0]
			acc.waiting += i64(r[0])
			acc.running += i64(r[1])
			acc.retry += i64(r[2])
			acc.failed += i64(r[3])
			acc.dead += i64(r[4])
			acc.enqMin += i64(r[5])
			acc.deqMin += i64(r[6])
			acc.enqHour += i64(r[7])
			acc.deqHour += i64(r[8])
			if age := i64(r[9]); age > acc.oldestAgeS {
				acc.oldestAgeS = age
			}
			return nil
		})
		if txErr != nil {
			return txErr
		}
	}

	depth := acc.waiting + acc.running + acc.retry
	return a.store.PlatformTx(ctx, func(c *pg.Conn) error {
		if err := upsertQueueStats(c, "queue_stats_1m", queue, minuteStart, depth, &acc, acc.enqMin, acc.deqMin); err != nil {
			return err
		}
		return upsertQueueStats(c, "queue_stats_1h", queue, hourStart, depth, &acc, acc.enqHour, acc.deqHour)
	})
}

// upsertQueueStats writes one queue_stats row with last-sample-wins REPLACE semantics.
func upsertQueueStats(c *pg.Conn, table, queue string, bucket time.Time, depth int64, acc *queueSample, enq, deq int64) error {
	return c.ExecParams(`insert into `+table+`
		(queue, bucket_start, depth, running, scheduled, delayed, retry, failed, dead, enq, deq, oldest_age_s)
		values ($1,$2,$3,$4,0,0,$5,$6,$7,$8,$9,$10)
		on conflict (queue, bucket_start) do update set
		  depth = excluded.depth, running = excluded.running, scheduled = excluded.scheduled,
		  delayed = excluded.delayed, retry = excluded.retry, failed = excluded.failed,
		  dead = excluded.dead, enq = excluded.enq, deq = excluded.deq, oldest_age_s = excluded.oldest_age_s`,
		queue, bucket, depth, acc.running, acc.retry, acc.failed, acc.dead, enq, deq, acc.oldestAgeS)
}
