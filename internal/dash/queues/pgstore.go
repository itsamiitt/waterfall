package queues

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Service is the queues feature's business layer: the engine-agnostic read model over
// job_outbox + queue_stats_1m + queue_defs, single redrive (delegated to pgoutbox), filtered
// bulk replay, and the scale-intent single-writer for queue_defs.desired_replicas.
type Service struct {
	store  *db.Store
	outbox OutboxRedriver
	audit  *audit.Log
	log    *slog.Logger
	now    func() time.Time

	replay      *replayer
	scaleIntent *metrics.Gauge // dash_worker_scale_intent{queue}
}

// Config wires the Service. InstanceID/Now/ReplayRatePerMin fall back to defaults.
type Config struct {
	Store            *db.Store
	Outbox           OutboxRedriver
	Audit            *audit.Log
	Metrics          *metrics.Registry
	Logger           *slog.Logger
	Now              func() time.Time
	InstanceID       string
	ReplayRatePerMin int
}

// NewService assembles the Service and its replay executor.
func NewService(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	reg := cfg.Metrics
	if reg == nil {
		reg = metrics.New()
	}
	s := &Service{
		store:       cfg.Store,
		outbox:      cfg.Outbox,
		audit:       cfg.Audit,
		log:         cfg.Logger,
		now:         cfg.Now,
		scaleIntent: reg.Gauge("dash_worker_scale_intent", "operator-recorded desired worker replicas per queue (intent; actuation is deploy-layer)", "queue"),
	}
	s.replay = newReplayer(s, cfg.InstanceID, cfg.ReplayRatePerMin)
	return s
}

// --- QueueStats: platform reads (queue_defs + queue_stats_1m, Class P) ---

// Queues returns every queue_defs row joined with its newest folded state-count vector. Live
// counts come from the last queue_stats_1m sample — never a per-request COUNT(*) over job_outbox
// (doc 06 §2.1). Platform-scoped: an operator's tx binds tenant='platform' and passes the
// platform_only policy; a customer Tenant reads zero rows.
func (s *Service) Queues(ctx context.Context) ([]QueueSummary, error) {
	var out []QueueSummary
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query(`select d.name, coalesce(d.kind,''), coalesce(d.max_attempts,0),
		        coalesce(d.visibility_s,0), coalesce(d.description,''), d.desired_replicas,
		        coalesce(s.depth,0), coalesce(s.running,0), coalesce(s.scheduled,0), coalesce(s.delayed,0),
		        coalesce(s.retry,0), coalesce(s.failed,0), coalesce(s.dead,0), coalesce(s.enq,0),
		        coalesce(s.deq,0), coalesce(s.oldest_age_s,0), s.bucket_start
		   from queue_defs d
		   left join lateral (
		     select depth, running, scheduled, delayed, retry, failed, dead, enq, deq, oldest_age_s, bucket_start
		       from queue_stats_1m q where q.queue = d.name order by q.bucket_start desc limit 1
		   ) s on true
		  order by d.name asc`)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			qs := QueueSummary{
				Name:        s0(r[0]),
				Kind:        s0(r[1]),
				MaxAttempts: int(i64(r[2])),
				VisibilityS: int(i64(r[3])),
				Description: s0(r[4]),
				Depth:       i64(r[6]), Running: i64(r[7]), Scheduled: i64(r[8]), Delayed: i64(r[9]),
				Retry: i64(r[10]), Failed: i64(r[11]), Dead: i64(r[12]), Enq: i64(r[13]),
				Deq: i64(r[14]), OldestAgeS: i64(r[15]),
			}
			if r[5] != nil {
				v := i64(r[5])
				qs.DesiredReplicas = &v
			}
			if r[16] != nil {
				qs.SampleAt = parseTS(s0(r[16]))
			}
			out = append(out, qs)
		}
		return nil
	})
	return out, err
}

// statsRetention is the doc 04 §1.8 / migration 0009 retention per queue_stats resolution.
func statsRetention(res string) time.Duration {
	if res == "1h" {
		return 30 * 24 * time.Hour
	}
	return 7 * 24 * time.Hour // 1m
}

// Stats returns queue_stats_<res> buckets for one queue in [from,to), ascending. It rejects an
// inverted window or one beyond retention (ErrWindowOutOfRange). Platform-scoped.
func (s *Service) Stats(ctx context.Context, queue string, w Window) ([]StatsBucket, error) {
	res := w.Res
	if res != "1m" && res != "1h" {
		res = "1m"
	}
	if !w.To.After(w.From) || w.From.Before(s.now().Add(-statsRetention(res))) {
		return nil, ErrWindowOutOfRange
	}
	table := "queue_stats_" + res
	var out []StatsBucket
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		r, qerr := c.QueryParams(`select bucket_start, depth, running, scheduled, delayed, retry,
		        failed, dead, enq, deq, oldest_age_s from `+table+`
		  where queue = $1 and bucket_start >= $2 and bucket_start < $3
		  order by bucket_start asc limit 5000`, queue, w.From.UTC(), w.To.UTC())
		if qerr != nil {
			return qerr
		}
		for _, row := range r.Rows {
			out = append(out, StatsBucket{
				BucketStart: parseTS(s0(row[0])),
				Depth:       i64(row[1]), Running: i64(row[2]), Scheduled: i64(row[3]), Delayed: i64(row[4]),
				Retry: i64(row[5]), Failed: i64(row[6]), Dead: i64(row[7]), Enq: i64(row[8]),
				Deq: i64(row[9]), OldestAgeS: i64(row[10]),
			})
		}
		return nil
	})
	return out, err
}

// --- JobLister: tenant-scoped reads over job_outbox ---

// statePredicate renders the exact job_outbox predicate for one engine-agnostic state
// (doc 06 §2.2). visSec is bound literally (a validated int) into the visibility interval.
func statePredicate(st State, visSec int) string {
	vis := fmt.Sprintf("now() - interval '%d seconds'", visSec)
	switch st {
	case StateWaiting:
		return "pending and not dead and claimed_at is null and attempts = 0"
	case StateRunning:
		return "pending and not dead and claimed_at is not null and claimed_at >= " + vis
	case StateRetry:
		return "pending and not dead and attempts >= 1 and (claimed_at is null or claimed_at < " + vis + ")"
	case StateFailed:
		return "not pending and not dead and status = 'failed'"
	case StateDead:
		return "dead"
	default: // scheduled, delayed — always 0 on pgoutbox
		return "false"
	}
}

// Jobs lists Enrichment Jobs in one required engine-agnostic state, keyset-paginated by
// (created_at, job_id) ascending. RLS scopes rows to the caller's Tenant (G1). queue is the
// logical name; pgoutbox is a single outbox, so it does not sub-filter rows.
func (s *Service) Jobs(ctx context.Context, queue string, state State, cur db.Cursor, limit int) ([]JobRow, db.Cursor, error) {
	if !ValidState(state) {
		return nil, db.Cursor{}, ErrInvalidFilter
	}
	limit = db.ClampLimit(limit)
	pred := statePredicate(state, defaultVisibilitySeconds)
	var out []JobRow
	var next db.Cursor
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		var r *pg.Result
		var qerr error
		base := `select job_id, status, attempts, dead, pending, claimed_at, created_at, updated_at
		         from job_outbox where ` + pred
		if len(cur.K) > 0 && cur.K[0] != "" {
			r, qerr = c.QueryParams(base+` and (created_at, job_id) > ($1::timestamptz, $2)
			  order by created_at asc, job_id asc limit $3`, cur.K[0], cur.ID, int64(limit+1))
		} else {
			r, qerr = c.QueryParams(base+` order by created_at asc, job_id asc limit $1`, int64(limit+1))
		}
		if qerr != nil {
			return qerr
		}
		rows := r.Rows
		if len(rows) > limit {
			last := rows[limit-1]
			next = db.Cursor{K: []string{s0(last[6])}, ID: s0(last[0])}
			rows = rows[:limit]
		}
		for _, row := range rows {
			out = append(out, JobRow{
				JobID:     s0(row[0]),
				State:     state,
				Status:    s0(row[1]),
				Attempts:  int(i64(row[2])),
				Dead:      b(row[3]),
				CreatedAt: parseTS(s0(row[6])),
				UpdatedAt: parseTS(s0(row[7])),
			})
		}
		return nil
	})
	if err != nil {
		return nil, db.Cursor{}, err
	}
	return out, next, nil
}

// DeadLetters lists parked jobs (dead=true), newest first (updated_at desc, job_id desc),
// filterable by error class / time bounds. Keyset-paginated, RLS-scoped (G1).
func (s *Service) DeadLetters(ctx context.Context, f DeadFilter, cur db.Cursor, limit int) ([]DeadLetterRow, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	where, args := deadWhere(f)
	var out []DeadLetterRow
	var next db.Cursor
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		q := `select job_id, status, attempts, coalesce(last_error,''), updated_at, created_at
		      from job_outbox where ` + where
		if len(cur.K) > 0 && cur.K[0] != "" {
			args = append(args, cur.K[0], cur.ID)
			q += fmt.Sprintf(" and (updated_at, job_id) < ($%d::timestamptz, $%d)", len(args)-1, len(args))
		}
		args = append(args, int64(limit+1))
		q += fmt.Sprintf(" order by updated_at desc, job_id desc limit $%d", len(args))
		r, qerr := c.QueryParams(q, args...)
		if qerr != nil {
			return qerr
		}
		rows := r.Rows
		if len(rows) > limit {
			last := rows[limit-1]
			next = db.Cursor{K: []string{s0(last[4])}, ID: s0(last[0])}
			rows = rows[:limit]
		}
		for _, row := range rows {
			out = append(out, DeadLetterRow{
				JobID:     s0(row[0]),
				Status:    s0(row[1]),
				Attempts:  int(i64(row[2])),
				LastError: s0(row[3]),
				UpdatedAt: parseTS(s0(row[4])),
				CreatedAt: parseTS(s0(row[5])),
			})
		}
		return nil
	})
	if err != nil {
		return nil, db.Cursor{}, err
	}
	return out, next, nil
}

// deadWhere builds the `dead`-scoped predicate + positional args for the DLQ filters.
func deadWhere(f DeadFilter) (string, []any) {
	parts := []string{"dead"}
	var args []any
	if f.ErrorClass != "" {
		args = append(args, "%"+f.ErrorClass+"%")
		parts = append(parts, fmt.Sprintf("last_error ilike $%d", len(args)))
	}
	if !f.Before.IsZero() {
		args = append(args, f.Before.UTC())
		parts = append(parts, fmt.Sprintf("updated_at < $%d", len(args)))
	}
	if !f.After.IsZero() {
		args = append(args, f.After.UTC())
		parts = append(parts, fmt.Sprintf("updated_at > $%d", len(args)))
	}
	return strings.Join(parts, " and "), args
}

// JobDetail returns the outbox row detail for GET /jobs/{id}: operational fields + a redacted
// request summary (never the captured Principal or secret material). 404 across Tenants (RLS).
func (s *Service) JobDetail(ctx context.Context, id string) (JobDetail, error) {
	var d JobDetail
	found := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		r, qerr := c.QueryParams(`select job_id, status, attempts, dead, pending, coalesce(last_error,''),
		        claimed_at, created_at, updated_at, payload
		   from job_outbox where job_id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(r.Rows) == 0 || r.Rows[0][0] == nil {
			return nil
		}
		row := r.Rows[0]
		found = true
		d = JobDetail{
			JobID:     s0(row[0]),
			Status:    s0(row[1]),
			Attempts:  int(i64(row[2])),
			Dead:      b(row[3]),
			Pending:   b(row[4]),
			LastError: s0(row[5]),
			CreatedAt: parseTS(s0(row[7])),
			UpdatedAt: parseTS(s0(row[8])),
		}
		if row[6] != nil {
			t := parseTS(s0(row[6]))
			d.ClaimedAt = &t
		}
		d.State = deriveState(d.Pending, d.Dead, d.ClaimedAt, d.Attempts, d.Status, s.now(), defaultVisibilitySeconds)
		if row[9] != nil {
			d.SubjectID, d.WantFields = redactPayload(*row[9])
		}
		return nil
	})
	if err != nil {
		return JobDetail{}, err
	}
	if !found {
		return JobDetail{}, ErrNotFound
	}
	return d, nil
}

// deriveState maps an outbox row's flags to the engine-agnostic state vector (doc 06 §2.2).
func deriveState(pending, dead bool, claimedAt *time.Time, attempts int, status string, now time.Time, visSec int) State {
	if dead {
		return StateDead
	}
	if !pending {
		if status == "failed" {
			return StateFailed
		}
		return StateFailed // terminal non-dead non-pending: only 'failed' remains operational
	}
	fresh := claimedAt != nil && !claimedAt.Before(now.Add(-time.Duration(visSec)*time.Second))
	if fresh {
		return StateRunning
	}
	if attempts >= 1 {
		return StateRetry
	}
	return StateWaiting
}

// redactPayload extracts ONLY the non-sensitive request summary from a serialized job.Job. The
// captured Principal, known attributes, and any values are never returned (doc 05 §7.3).
func redactPayload(payload string) (subjectID string, want []string) {
	var pl struct {
		Req struct {
			Subject struct {
				ID string `json:"ID"`
			} `json:"Subject"`
			Want []string `json:"Want"`
		} `json:"Req"`
	}
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		return "", nil
	}
	return pl.Req.Subject.ID, pl.Req.Want
}

// --- write verbs ---

// Redrive re-delivers ONE parked job by delegating to pgoutbox (one-owner-per-table). Returns
// false (idempotent no-op) when no dead row matched — a double-click or an absent/foreign job.
func (s *Service) Redrive(ctx context.Context, jobID string) (bool, error) {
	ok, err := s.outbox.Redrive(ctx, jobID)
	if err != nil {
		return false, err
	}
	if ok {
		s.appendAudit(ctx, "queue_redrive", "job_outbox", jobID, map[string]any{"redriven": true})
	}
	return ok, nil
}

// Replay starts a filtered bulk redrive as an async 202 bulk job and returns its id. The filter
// is re-evaluated under RLS at execution (doc 06 §3.4).
func (s *Service) Replay(ctx context.Context, queue string, f DeadFilter) (string, error) {
	return s.replay.submit(ctx, queue, f)
}

// ReplayStatus returns a bulk-replay job's durable progress (replay kind only), RLS-scoped.
func (s *Service) ReplayStatus(ctx context.Context, id string) (ReplayJob, bool, error) {
	return s.replay.status(ctx, id)
}

// BulkJobStatus returns any durable bulk_jobs row (kind-agnostic) — the single poller behind
// GET /bulk-jobs/{id} for replay and rolling_restart alike. RLS-scoped (404 across Tenants).
func (s *Service) BulkJobStatus(ctx context.Context, id string) (BulkJob, bool, error) {
	return s.replay.bulkJobStatus(ctx, id)
}

// SetScaleIntent is the single writer of queue_defs.desired_replicas (doc 06 §5, OI-QW-3): both
// PUT /queues/{name}/workers and POST /workers/scale route through here. It records intent +
// emits the dash_worker_scale_intent gauge; actuation is deploy-layer. replicas<0 is rejected.
func (s *Service) SetScaleIntent(ctx context.Context, queue string, replicas int) error {
	if queue == "" || replicas < 0 {
		return ErrInvalidFilter
	}
	by := actorUUID(ctx)
	if err := s.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into queue_defs (name, desired_replicas, replicas_updated_at, replicas_updated_by)
			values ($1, $2, now(), $3::uuid)
			on conflict (name) do update set
			  desired_replicas = excluded.desired_replicas,
			  replicas_updated_at = excluded.replicas_updated_at,
			  replicas_updated_by = excluded.replicas_updated_by`, queue, int64(replicas), by)
	}); err != nil {
		return err
	}
	s.scaleIntent.Set(float64(replicas), queue)
	s.appendAudit(ctx, "queue_scale_intent", "queue_defs", queue, map[string]any{"desired_replicas": replicas})
	return nil
}

// --- helpers ---

// appendAudit writes one redacted audit row (best-effort, matching the keys/providers pattern).
func (s *Service) appendAudit(ctx context.Context, action, kind, objectID string, after map[string]any) {
	if s.audit == nil {
		return
	}
	e := audit.Entry{Action: action, ObjectKind: kind, ObjectID: objectID, After: rawJSON(after)}
	if p, err := tenant.FromContext(ctx); err == nil {
		e.ActorUserID = p.UserID
		e.ActorRole = db.RoleFromPrincipal(p)
	}
	if err := s.audit.Append(ctx, e); err != nil {
		s.log.Error("audit append failed", "action", action, "err", err)
	}
}

// actorUUID returns the acting user id if it is a UUID (queue_defs.replicas_updated_by is uuid),
// else nil so the column stays NULL — the audit chain records the actor regardless.
func actorUUID(ctx context.Context) any {
	p, err := tenant.FromContext(ctx)
	if err != nil || !looksLikeUUID(p.UserID) {
		return nil
	}
	return p.UserID
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

func rawJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// --- nullable column scanners (kept local; queues stays self-contained) ---

func s0(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(*p), 10, 64)
	return n
}

func b(p *string) bool { return p != nil && (*p == "t" || *p == "true") }

func parseTS(str string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999-07:00",
	} {
		if t, err := time.Parse(layout, str); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
