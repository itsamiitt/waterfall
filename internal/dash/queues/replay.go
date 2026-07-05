package queues

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// bulkKindReplay is the bulk_jobs.kind for a filtered replay (doc 06 §3.4).
const bulkKindReplay = "replay"

// replayLease is the executor's liveness lease on its bulk_jobs row (doc 04 §4.1). It is renewed
// on each progress commit; on expiry the janitor re-queues the row for a successor.
const replayLease = 60 * time.Second

// replayPageSize is the keyset batch of dead rows the executor redrives per iteration.
const replayPageSize = 200

// resultsCap bounds the per-item results persisted on the job (counts stay exact; doc 06 §3.4).
const resultsCap = 1000

// replayer executes filtered bulk replays as durable bulk_jobs. The storm guard (one active
// replay per queue) is the bulk_jobs one-in-flight partial unique index; the per-queue redrive
// token bucket is in-memory in this single-instance executor (correct by construction — exactly
// one instance holds the job's lease, doc 06 §3.4).
type replayer struct {
	svc        *Service
	instanceID string
	ratePerMin int
}

func newReplayer(svc *Service, instanceID string, ratePerMin int) *replayer {
	if instanceID == "" {
		instanceID = "dashboardd-" + shortID()
	}
	if ratePerMin <= 0 {
		ratePerMin = 600 // doc 06 §3.4 default
	}
	return &replayer{svc: svc, instanceID: instanceID, ratePerMin: ratePerMin}
}

// submit inserts the queued bulk_jobs row (the one-in-flight unique index enforces the storm
// guard) and launches the detached executor. It returns the job id, or ErrReplayInFlight when a
// replay is already active for this queue.
func (r *replayer) submit(ctx context.Context, queue string, f DeadFilter) (string, error) {
	if queue == "" {
		return "", ErrInvalidFilter
	}
	id := uuidV4()
	by := actorUUID(ctx)
	err := r.svc.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into bulk_jobs (id, tenant_id, kind, scope_fingerprint, status, created_by)
			values ($1::uuid, current_setting('app.current_tenant'), $2, $3, 'queued', $4::uuid)`,
			id, bulkKindReplay, queue, by)
	})
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrReplayInFlight
		}
		return "", err
	}
	r.svc.appendAudit(ctx, "queue_replay", "bulk_jobs", id, map[string]any{
		"queue": queue, "error_class": f.ErrorClass,
	})
	bg := detach(ctx)
	go r.execute(bg, id, queue, f)
	return id, nil
}

// execute claims the row and drives the replay to a terminal state. It is crash-tolerant by the
// bulk_jobs lease/janitor model (doc 04 §4.1): a dead instance's lease expires and a successor
// re-claims and resumes (redrive is an idempotent per-row transition).
func (r *replayer) execute(ctx context.Context, id, queue string, f DeadFilter) {
	claimed, err := r.claim(ctx, id)
	if err != nil || !claimed {
		return
	}
	bucket := newTokenBucket(r.ratePerMin, r.svc.now)
	var results []ReplayItem
	matched, succeeded, failed := 0, 0, 0
	afterID := ""
	for {
		ids, err := r.deadPage(ctx, f, afterID)
		if err != nil {
			r.svc.log.Error("replay page failed", "job_id", id, "err", err)
			break
		}
		if len(ids) == 0 {
			break
		}
		for _, jobID := range ids {
			bucket.take()
			matched++
			afterID = jobID
			ok, rerr := r.svc.outbox.Redrive(ctx, jobID)
			outcome := OutcomeRedriven
			switch {
			case rerr != nil:
				outcome = OutcomeError
				failed++
			case ok:
				succeeded++
			default:
				outcome = OutcomeSkippedNotDead
			}
			if len(results) < resultsCap {
				results = append(results, ReplayItem{JobID: jobID, Outcome: outcome})
			}
		}
		if err := r.progress(ctx, id, matched, succeeded, failed, results); err != nil {
			r.svc.log.Error("replay progress failed", "job_id", id, "err", err)
		}
	}
	status := "succeeded"
	if failed > 0 {
		status = "partial"
	}
	if err := r.finish(ctx, id, status, matched, succeeded, failed, results); err != nil {
		r.svc.log.Error("replay finish failed", "job_id", id, "err", err)
	}
}

// claim transitions the queued row to running under this instance's lease. ok=false when another
// instance already claimed it.
func (r *replayer) claim(ctx context.Context, id string) (bool, error) {
	ok := false
	err := r.svc.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`update bulk_jobs set status='running', claimed_by=$2,
			lease_expires_at=now() + interval '`+leaseInterval()+`', started_at=now(), attempts=attempts+1
		  where id=$1::uuid and status='queued' returning id`, id, r.instanceID)
		if qerr != nil {
			return qerr
		}
		ok = len(res.Rows) > 0 && res.Rows[0][0] != nil
		return nil
	})
	return ok, err
}

// deadPage returns the next keyset batch of matching dead job ids (job_id ascending). Redriven
// rows leave the dead set, so advancing the job_id cursor terminates without revisiting.
func (r *replayer) deadPage(ctx context.Context, f DeadFilter, afterID string) ([]string, error) {
	where, args := deadWhere(f)
	var ids []string
	err := r.svc.store.Tx(ctx, func(c *pg.Conn) error {
		q := `select job_id from job_outbox where ` + where
		if afterID != "" {
			args = append(args, afterID)
			q += fmt.Sprintf(" and job_id > $%d", len(args))
		}
		args = append(args, int64(replayPageSize))
		q += fmt.Sprintf(" order by job_id asc limit $%d", len(args))
		res, qerr := c.QueryParams(q, args...)
		if qerr != nil {
			return qerr
		}
		for _, row := range res.Rows {
			ids = append(ids, s0(row[0]))
		}
		return nil
	})
	return ids, err
}

func (r *replayer) progress(ctx context.Context, id string, matched, succeeded, failed int, results []ReplayItem) error {
	payload, _ := json.Marshal(results)
	return r.svc.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update bulk_jobs set total=$2, succeeded=$3, failed=$4,
			matched_at_execution=$2, results=$5::jsonb, lease_expires_at=now() + interval '`+leaseInterval()+`'
		  where id=$1::uuid`, id, int64(matched), int64(succeeded), int64(failed), string(payload))
	})
}

func (r *replayer) finish(ctx context.Context, id, status string, matched, succeeded, failed int, results []ReplayItem) error {
	payload, _ := json.Marshal(results)
	return r.svc.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update bulk_jobs set status=$2, total=$3, succeeded=$4, failed=$5,
			matched_at_execution=$3, results=$6::jsonb, finished_at=now(), lease_expires_at=null
		  where id=$1::uuid`, id, status, int64(matched), int64(succeeded), int64(failed), string(payload))
	})
}

// status reads a replay bulk_jobs row (RLS-scoped: 404 across Tenants).
func (r *replayer) status(ctx context.Context, id string) (ReplayJob, bool, error) {
	var j ReplayJob
	found := false
	err := r.svc.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id, kind, status, total, succeeded, failed,
			coalesce(matched_at_execution,0), created_at, started_at, finished_at, results
		  from bulk_jobs where id=$1::uuid and kind=$2`, id, bulkKindReplay)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		found = true
		row := res.Rows[0]
		j = ReplayJob{
			ID: s0(row[0]), Kind: s0(row[1]), Status: s0(row[2]),
			Total: int(i64(row[3])), Succeeded: int(i64(row[4])), Failed: int(i64(row[5])),
			MatchedAtExecution: int(i64(row[6])), CreatedAt: parseTS(s0(row[7])),
		}
		if row[8] != nil {
			t := parseTS(s0(row[8]))
			j.StartedAt = &t
		}
		if row[9] != nil {
			t := parseTS(s0(row[9]))
			j.FinishedAt = &t
		}
		if row[10] != nil {
			_ = json.Unmarshal([]byte(*row[10]), &j.Results)
		}
		return nil
	})
	if err != nil {
		return ReplayJob{}, false, err
	}
	return j, found, nil
}

// bulkJobStatus reads any bulk_jobs row (kind-agnostic) — the single durable poller behind
// GET /bulk-jobs/{id} for every 202 operation. RLS-scoped (404 across Tenants).
func (r *replayer) bulkJobStatus(ctx context.Context, id string) (BulkJob, bool, error) {
	var j BulkJob
	found := false
	err := r.svc.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id, kind, status, total, succeeded, failed,
			coalesce(matched_at_execution,0), created_at, started_at, finished_at, results
		  from bulk_jobs where id=$1::uuid`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		found = true
		row := res.Rows[0]
		j = BulkJob{
			ID: s0(row[0]), Kind: s0(row[1]), Status: s0(row[2]),
			Total: int(i64(row[3])), Succeeded: int(i64(row[4])), Failed: int(i64(row[5])),
			MatchedAtExecution: int(i64(row[6])), CreatedAt: parseTS(s0(row[7])),
		}
		if row[8] != nil {
			t := parseTS(s0(row[8]))
			j.StartedAt = &t
		}
		if row[9] != nil {
			t := parseTS(s0(row[9]))
			j.FinishedAt = &t
		}
		if row[10] != nil {
			j.Results = []byte(*row[10])
		}
		return nil
	})
	if err != nil {
		return BulkJob{}, false, err
	}
	return j, found, nil
}

// --- token bucket (per-queue redrive rate cap, in-memory in the single-instance executor) ---

type tokenBucket struct {
	capacity float64
	tokens   float64
	perSec   float64
	last     time.Time
	now      func() time.Time
}

func newTokenBucket(perMin int, now func() time.Time) *tokenBucket {
	cap := float64(perMin)
	return &tokenBucket{capacity: cap, tokens: cap, perSec: cap / 60, last: now(), now: now}
}

// take consumes one token, sleeping only when the bucket is empty so a large replay refills the
// backlog gradually instead of spiking depth (F5 storm guard, doc 06 §3.4).
func (t *tokenBucket) take() {
	now := t.now()
	t.tokens += now.Sub(t.last).Seconds() * t.perSec
	if t.tokens > t.capacity {
		t.tokens = t.capacity
	}
	t.last = now
	if t.tokens < 1 {
		wait := time.Duration((1 - t.tokens) / t.perSec * float64(time.Second))
		time.Sleep(wait)
		t.tokens = 0
		t.last = t.now()
		return
	}
	t.tokens--
}

// --- small helpers ---

func leaseInterval() string {
	return fmt.Sprintf("%d seconds", int(replayLease.Seconds()))
}

// detach returns a background context carrying the request Principal so the async executor keeps
// its Tenant identity (RLS) after the request context is cancelled.
func detach(ctx context.Context) context.Context {
	if p, err := tenant.FromContext(ctx); err == nil {
		return tenant.WithPrincipal(context.Background(), p)
	}
	return context.Background()
}

func isUniqueViolation(err error) bool {
	var pe *pg.PGError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// uuidV4 returns a random RFC-4122 v4 UUID (stdlib-only) for bulk_jobs.id.
func uuidV4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func shortID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}
