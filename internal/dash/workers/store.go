// Package workers is the dashboard's worker-management feature (module 9, doc 04 §2.9, doc 06
// §4/§5): the enrichd worker registry, the 10s heartbeat handler, the desired-state convergence
// actions (restart/drain/pause/resume), scale intent, rolling restart, and the server-derived
// worker-lost detector.
//
// Declared-vs-derived doctrine: the dashboard WRITES intent (workers.desired_state) and workers
// REPORT actual status via heartbeat; the two columns are rendered side by side with a
// "converging" badge while they differ. The heartbeat channel is the ONLY control channel — no
// SSH, no exec (doc 06 §4). `lost` is SERVER-derived (a crashed worker cannot report its death).
//
// Gates: workers is Class P (platform-only FORCE RLS) — every access binds tenant='platform'
// via db.Store from an operator Principal. No PII/secrets in logs.
package workers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// ErrNotFound is returned when no worker row matches (404, or 0 rows under RLS).
var ErrNotFound = errors.New("workers: not found")

// Desired-state vocabulary (workers.desired_state CHECK, migration 0008).
const (
	DesiredRunning  = "running"
	DesiredDraining = "draining"
	DesiredPaused   = "paused"
	DesiredStopped  = "stopped"
)

// Status vocabulary (workers.status CHECK).
const (
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusDraining = "draining"
	StatusPaused   = "paused"
	StatusStopped  = "stopped"
	StatusLost     = "lost"
)

// Beat is one heartbeat's reported state (doc 06 §4: every 10s).
type Beat struct {
	ID         string
	Kind       string
	Region     string
	Queue      string
	Version    string
	Status     string
	CPUPct     float64
	MemMB      float64
	JobsActive int
	JobsDone   int64
}

// WorkerRow is a registry row: reported status + written desired_state (both surfaced).
type WorkerRow struct {
	ID              string
	Kind            string
	Region          string
	Queue           string
	Version         string
	Status          string
	DesiredState    string
	StartedAt       *time.Time
	LastHeartbeatAt *time.Time
	CPUPct          float64
	MemMB           float64
	JobsActive      int
	JobsDone        int64
	Restarts        int
}

// WorkerFilter narrows the fleet list (doc 04 §2.9).
type WorkerFilter struct {
	Kind, Queue, Region, Status string
}

// OverdueWorker is a heartbeat-overdue candidate the detector evaluates.
type OverdueWorker struct {
	ID       string
	LastBeat time.Time
}

// --- consumer-side interfaces (repo style: small ifaces + var _ assertions) ---

// Registry is what the HTTP handlers need: heartbeat upsert, fleet list/detail, and the
// desired-state writes.
type Registry interface {
	Upsert(ctx context.Context, b Beat, now time.Time) (WorkerRow, error)
	List(ctx context.Context, f WorkerFilter, cur db.Cursor, limit int) ([]WorkerRow, db.Cursor, error)
	Get(ctx context.Context, id string) (WorkerRow, bool, error)
	SetDesiredState(ctx context.Context, id, desired string, restart bool, now time.Time) (WorkerRow, error)
}

// LostStore is what the worker-lost detector needs: overdue candidates and the lost transition.
type LostStore interface {
	OverdueWorkers(ctx context.Context, cutoff time.Time) ([]OverdueWorker, error)
	MarkLost(ctx context.Context, id string) (bool, error)
}

// ScaleIntentSetter is the single writer of queue_defs.desired_replicas (owned by the queues
// feature — one-owner-per-table). POST /workers/scale delegates to it (doc 06 §5).
type ScaleIntentSetter interface {
	SetScaleIntent(ctx context.Context, queue string, replicas int) error
}

// Store is the Postgres-backed registry + lost store over the shared db.Store.
type Store struct{ store *db.Store }

var (
	_ Registry  = (*Store)(nil)
	_ LostStore = (*Store)(nil)
)

// NewStore wraps the shared db.Store.
func NewStore(store *db.Store) *Store { return &Store{store: store} }

// Upsert applies a heartbeat: it inserts a fresh registration or updates the reported columns and
// stamps last_heartbeat_at, RETURNING the full row (so the handler can echo desired_state). A
// worker that boots after a restart (status='starting') with a pending restart marker converges
// desired_state back to 'running' and clears the marker (doc 06 §5 restart row).
func (s *Store) Upsert(ctx context.Context, b Beat, now time.Time) (WorkerRow, error) {
	status := b.Status
	if status == "" {
		status = StatusRunning
	}
	var row WorkerRow
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`insert into workers
			(id, kind, region, queue, version, status, desired_state, started_at, last_heartbeat_at,
			 cpu_pct, mem_mb, jobs_active, jobs_done)
			values ($1,$2,$3,$4,$5,$6,'running',$7,$7,$8,$9,$10,$11)
			on conflict (id) do update set
			  kind = coalesce(nullif(excluded.kind,''), workers.kind),
			  region = coalesce(nullif(excluded.region,''), workers.region),
			  queue = coalesce(nullif(excluded.queue,''), workers.queue),
			  version = coalesce(nullif(excluded.version,''), workers.version),
			  status = excluded.status,
			  last_heartbeat_at = excluded.last_heartbeat_at,
			  cpu_pct = excluded.cpu_pct, mem_mb = excluded.mem_mb,
			  jobs_active = excluded.jobs_active, jobs_done = excluded.jobs_done
			returning id, coalesce(kind,''), coalesce(region,''), coalesce(queue,''), coalesce(version,''),
			  status, desired_state, started_at, last_heartbeat_at, coalesce(cpu_pct,0), coalesce(mem_mb,0),
			  jobs_active, jobs_done, restarts, coalesce(attrs->>'restart','')`,
			b.ID, b.Kind, b.Region, b.Queue, b.Version, status, now,
			b.CPUPct, b.MemMB, b.JobsActive, b.JobsDone)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		row = scanWorker(res.Rows[0])
		restartMarker := s0(res.Rows[0][14]) == "true"
		// Restart convergence: a relaunched worker (booting) with a pending restart marker resets
		// desired_state to running and clears the marker.
		if restartMarker && row.DesiredState == DesiredStopped &&
			(status == StatusStarting || status == StatusRunning) {
			r2, uerr := c.QueryParams(`update workers set desired_state='running',
				attrs = coalesce(attrs,'{}'::jsonb) - 'restart'
			  where id=$1 returning desired_state`, b.ID)
			if uerr != nil {
				return uerr
			}
			if len(r2.Rows) > 0 {
				row.DesiredState = s0(r2.Rows[0][0])
			}
		}
		return nil
	})
	if err != nil {
		return WorkerRow{}, err
	}
	return row, nil
}

// List returns the fleet, keyset-paginated by id ascending, filtered by kind/queue/region/status.
func (s *Store) List(ctx context.Context, f WorkerFilter, cur db.Cursor, limit int) ([]WorkerRow, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	var args []any
	parts := []string{"true"}
	add := func(col, val string) {
		if val != "" {
			args = append(args, val)
			parts = append(parts, fmt.Sprintf("%s = $%d", col, len(args)))
		}
	}
	add("kind", f.Kind)
	add("queue", f.Queue)
	add("region", f.Region)
	add("status", f.Status)
	if cur.ID != "" {
		args = append(args, cur.ID)
		parts = append(parts, fmt.Sprintf("id > $%d", len(args)))
	}
	args = append(args, int64(limit+1))
	q := `select id, coalesce(kind,''), coalesce(region,''), coalesce(queue,''), coalesce(version,''),
		status, desired_state, started_at, last_heartbeat_at, coalesce(cpu_pct,0), coalesce(mem_mb,0),
		jobs_active, jobs_done, restarts, '' from workers where ` + strings.Join(parts, " and ") +
		fmt.Sprintf(" order by id asc limit $%d", len(args))
	var out []WorkerRow
	var next db.Cursor
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(q, args...)
		if qerr != nil {
			return qerr
		}
		rows := res.Rows
		if len(rows) > limit {
			next = db.Cursor{ID: s0(rows[limit-1][0])}
			rows = rows[:limit]
		}
		for _, r := range rows {
			out = append(out, scanWorker(r))
		}
		return nil
	})
	if err != nil {
		return nil, db.Cursor{}, err
	}
	return out, next, nil
}

// Get returns one worker row (ok=false when absent / not visible under RLS).
func (s *Store) Get(ctx context.Context, id string) (WorkerRow, bool, error) {
	var row WorkerRow
	found := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id, coalesce(kind,''), coalesce(region,''), coalesce(queue,''),
			coalesce(version,''), status, desired_state, started_at, last_heartbeat_at, coalesce(cpu_pct,0),
			coalesce(mem_mb,0), jobs_active, jobs_done, restarts, '' from workers where id=$1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		found = true
		row = scanWorker(res.Rows[0])
		return nil
	})
	if err != nil {
		return WorkerRow{}, false, err
	}
	return row, found, nil
}

// SetDesiredState writes the intent column (the ONLY worker mutation; doc 06 §5). restart also
// stamps a restart marker in attrs and bumps restarts. Returns the updated row or ErrNotFound.
func (s *Store) SetDesiredState(ctx context.Context, id, desired string, restart bool, now time.Time) (WorkerRow, error) {
	var row WorkerRow
	found := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		var res *pg.Result
		var qerr error
		if restart {
			res, qerr = c.QueryParams(`update workers set desired_state=$2,
				attrs = coalesce(attrs,'{}'::jsonb) || jsonb_build_object('restart','true','restart_at',$3::text),
				restarts = restarts + 1
			  where id=$1
			  returning id, coalesce(kind,''), coalesce(region,''), coalesce(queue,''), coalesce(version,''),
			    status, desired_state, started_at, last_heartbeat_at, coalesce(cpu_pct,0), coalesce(mem_mb,0),
			    jobs_active, jobs_done, restarts, ''`, id, desired, now.UTC().Format(time.RFC3339))
		} else {
			res, qerr = c.QueryParams(`update workers set desired_state=$2
			  where id=$1
			  returning id, coalesce(kind,''), coalesce(region,''), coalesce(queue,''), coalesce(version,''),
			    status, desired_state, started_at, last_heartbeat_at, coalesce(cpu_pct,0), coalesce(mem_mb,0),
			    jobs_active, jobs_done, restarts, ''`, id, desired)
		}
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		found = true
		row = scanWorker(res.Rows[0])
		return nil
	})
	if err != nil {
		return WorkerRow{}, err
	}
	if !found {
		return WorkerRow{}, ErrNotFound
	}
	return row, nil
}

// OverdueWorkers returns registered workers whose last_heartbeat_at predates cutoff and whose
// status is still a live one (never already lost/stopped). The detector applies hysteresis.
func (s *Store) OverdueWorkers(ctx context.Context, cutoff time.Time) ([]OverdueWorker, error) {
	var out []OverdueWorker
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id, last_heartbeat_at from workers
			where status in ('starting','running','paused','draining')
			  and last_heartbeat_at is not null and last_heartbeat_at < $1
			order by id asc`, cutoff.UTC())
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			out = append(out, OverdueWorker{ID: s0(r[0]), LastBeat: parseTS(s0(r[1]))})
		}
		return nil
	})
	return out, err
}

// MarkLost transitions a worker to status='lost' (server-derived). ok=false when the row was
// already lost / gone (so the detector fires the alert exactly once per episode).
func (s *Store) MarkLost(ctx context.Context, id string) (bool, error) {
	changed := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`update workers set status='lost'
			where id=$1 and status <> 'lost' returning id`, id)
		if qerr != nil {
			return qerr
		}
		changed = len(res.Rows) > 0 && res.Rows[0][0] != nil
		return nil
	})
	return changed, err
}

// RecordBeat persists the raw worker_heartbeats row (24h retention) and additively folds the
// worker_stats_5m bucket (doc 06 §4). Best-effort telemetry: called after Upsert.
func (s *Store) RecordBeat(ctx context.Context, b Beat, now time.Time) error {
	bucket := now.UTC().Truncate(5 * time.Minute)
	return s.store.Tx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(`insert into worker_heartbeats
			(worker_id, beat_at, status, cpu_pct, mem_mb, jobs_active, jobs_done)
			values ($1,$2,$3,$4,$5,$6,$7) on conflict (worker_id, beat_at) do nothing`,
			b.ID, now.UTC(), b.Status, b.CPUPct, b.MemMB, b.JobsActive, b.JobsDone); err != nil {
			return err
		}
		return c.ExecParams(`insert into worker_stats_5m
			(worker_id, bucket_start, beats, cpu_pct_sum, mem_mb_sum, jobs_active_max, jobs_done_delta)
			values ($1,$2,1,$3,$4,$5,0)
			on conflict (worker_id, bucket_start) do update set
			  beats = worker_stats_5m.beats + 1,
			  cpu_pct_sum = worker_stats_5m.cpu_pct_sum + excluded.cpu_pct_sum,
			  mem_mb_sum = worker_stats_5m.mem_mb_sum + excluded.mem_mb_sum,
			  jobs_active_max = greatest(worker_stats_5m.jobs_active_max, excluded.jobs_active_max)`,
			b.ID, bucket, b.CPUPct, b.MemMB, b.JobsActive)
	})
}

// MatchWorkers returns the deterministically-ordered ids of live workers matching kind/queue
// (empty filter = any), for rolling restart. status='stopped' workers are excluded.
func (s *Store) MatchWorkers(ctx context.Context, kind, queue string) ([]string, error) {
	var out []string
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id from workers
			where ($1 = '' or kind = $1) and ($2 = '' or queue = $2) and status <> 'stopped'
			order by id asc`, kind, queue)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			out = append(out, s0(r[0]))
		}
		return nil
	})
	return out, err
}

// WorkerStat is one worker_stats_5m bucket (averages computed at read: sum/beats).
type WorkerStat struct {
	BucketStart   time.Time
	Beats         int
	CPUPctAvg     float64
	MemMBAvg      float64
	JobsActiveMax int
	JobsDoneDelta int64
}

// Stats returns worker_stats_5m buckets for id in [from,to), ascending (GET /workers/{id}/stats).
func (s *Store) Stats(ctx context.Context, id string, from, to time.Time) ([]WorkerStat, error) {
	var out []WorkerStat
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select bucket_start, beats, cpu_pct_sum, mem_mb_sum, jobs_active_max, jobs_done_delta
			from worker_stats_5m where worker_id=$1 and bucket_start >= $2 and bucket_start < $3
			order by bucket_start asc limit 5000`, id, from.UTC(), to.UTC())
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			beats := int(i64(r[1]))
			st := WorkerStat{
				BucketStart: parseTS(s0(r[0])), Beats: beats,
				JobsActiveMax: int(i64(r[4])), JobsDoneDelta: i64(r[5]),
			}
			if beats > 0 {
				st.CPUPctAvg = f64(r[2]) / float64(beats)
				st.MemMBAvg = f64(r[3]) / float64(beats)
			}
			out = append(out, st)
		}
		return nil
	})
	return out, err
}

// --- scanning helpers ---

func scanWorker(r []*string) WorkerRow {
	w := WorkerRow{
		ID: s0(r[0]), Kind: s0(r[1]), Region: s0(r[2]), Queue: s0(r[3]), Version: s0(r[4]),
		Status: s0(r[5]), DesiredState: s0(r[6]), CPUPct: f64(r[9]), MemMB: f64(r[10]),
		JobsActive: int(i64(r[11])), JobsDone: i64(r[12]), Restarts: int(i64(r[13])),
	}
	if r[7] != nil {
		t := parseTS(s0(r[7]))
		w.StartedAt = &t
	}
	if r[8] != nil {
		t := parseTS(s0(r[8]))
		w.LastHeartbeatAt = &t
	}
	return w
}

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

func f64(p *string) float64 {
	if p == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(*p), 64)
	return v
}

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
