package workers

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

// ErrInFlight is returned when a rolling restart is already active for the same scope (409).
var ErrInFlight = errors.New("workers: a rolling restart is already in flight for this scope")

const bulkKindRollingRestart = "rolling_restart"

// RollingJob is a rolling-restart bulk job's durable progress (doc 06 §5).
type RollingJob struct {
	ID         string
	Status     string
	Total      int
	Succeeded  int
	Failed     int
	Results    []RollingItem
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// RollingItem is one worker's outcome in a wave: restarted | timeout.
type RollingItem struct {
	WorkerID string `json:"worker_id"`
	Wave     int    `json:"wave"`
	Outcome  string `json:"outcome"`
}

// rollingExec drives Deployment-style staged drain-first restarts (doc 06 §5). Wave progress is
// durable in the bulk_jobs row (crash-safe: a successor re-reads results and resumes; the wave
// verb — set desired_state=draining+restart — is an idempotent per-worker transition).
type rollingExec struct {
	svc            *Service
	instanceID     string
	perWaveTimeout time.Duration
	pollInterval   time.Duration
}

func newRollingExec(svc *Service, instanceID string) *rollingExec {
	if instanceID == "" {
		instanceID = "dashboardd-" + shortID()
	}
	return &rollingExec{svc: svc, instanceID: instanceID, perWaveTimeout: 90 * time.Second, pollInterval: time.Second}
}

// planWaves splits the deterministically-ordered worker set into waves of at most maxUnavailable
// (a pure function — the wave contract is unit-tested without a fleet).
func planWaves(ids []string, maxUnavailable int) [][]string {
	if maxUnavailable < 1 {
		maxUnavailable = 1
	}
	var waves [][]string
	for i := 0; i < len(ids); i += maxUnavailable {
		end := i + maxUnavailable
		if end > len(ids) {
			end = len(ids)
		}
		waves = append(waves, ids[i:end])
	}
	return waves
}

func (e *rollingExec) submit(ctx context.Context, kind, queue string, maxUnavailable int) (string, error) {
	if maxUnavailable < 1 {
		maxUnavailable = 1
	}
	id := uuidV4()
	fingerprint := kind + "/" + queue
	err := e.svc.dbStore.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into bulk_jobs (id, tenant_id, kind, scope_fingerprint, status)
			values ($1::uuid, current_setting('app.current_tenant'), $2, $3, 'queued')`,
			id, bulkKindRollingRestart, fingerprint)
	})
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrInFlight
		}
		return "", err
	}
	e.svc.appendAudit(ctx, "worker_rolling_restart", "bulk_jobs", id, map[string]any{
		"kind": kind, "queue": queue, "max_unavailable": maxUnavailable,
	})
	bg := detach(ctx)
	go e.execute(bg, id, kind, queue, maxUnavailable)
	return id, nil
}

func (e *rollingExec) execute(ctx context.Context, id, kind, queue string, maxUnavailable int) {
	if !e.claim(ctx, id) {
		return
	}
	ids, err := e.svc.store.MatchWorkers(ctx, kind, queue)
	if err != nil {
		e.svc.log.Error("rolling restart: match workers", "job", id, "err", err)
		e.finish(ctx, id, "failed", nil)
		return
	}
	waves := planWaves(ids, maxUnavailable)
	var results []RollingItem
	for wi, wave := range waves {
		waveStart := e.svc.now()
		for _, wid := range wave {
			if _, err := e.svc.store.SetDesiredState(ctx, wid, DesiredDraining, true, e.svc.now()); err != nil {
				e.svc.log.Warn("rolling restart: set draining", "worker", wid, "err", err)
			}
		}
		for _, wid := range wave {
			outcome := "timeout"
			if e.awaitConverge(ctx, wid, waveStart) {
				outcome = "restarted"
			}
			results = append(results, RollingItem{WorkerID: wid, Wave: wi, Outcome: outcome})
		}
		if err := e.progress(ctx, id, results); err != nil {
			e.svc.log.Warn("rolling restart: progress", "job", id, "err", err)
		}
	}
	status := "succeeded"
	for _, r := range results {
		if r.Outcome != "restarted" {
			status = "partial"
			break
		}
	}
	e.finish(ctx, id, status, results)
}

// awaitConverge polls until wid re-registers running (desired reset) with a beat after waveStart,
// or the per-wave timeout elapses.
func (e *rollingExec) awaitConverge(ctx context.Context, wid string, waveStart time.Time) bool {
	deadline := e.svc.now().Add(e.perWaveTimeout)
	for e.svc.now().Before(deadline) {
		row, ok, err := e.svc.store.Get(ctx, wid)
		if err == nil && ok && row.Status == StatusRunning && row.DesiredState == DesiredRunning &&
			row.LastHeartbeatAt != nil && row.LastHeartbeatAt.After(waveStart) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(e.pollInterval):
		}
	}
	return false
}

func (e *rollingExec) claim(ctx context.Context, id string) bool {
	ok := false
	_ = e.svc.dbStore.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`update bulk_jobs set status='running', claimed_by=$2,
			lease_expires_at=now() + interval '120 seconds', started_at=now(), attempts=attempts+1
		  where id=$1::uuid and status='queued' returning id`, id, e.instanceID)
		if err != nil {
			return err
		}
		ok = len(res.Rows) > 0 && res.Rows[0][0] != nil
		return nil
	})
	return ok
}

func (e *rollingExec) progress(ctx context.Context, id string, results []RollingItem) error {
	payload, _ := json.Marshal(results)
	succ, fail := tally(results)
	return e.svc.dbStore.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update bulk_jobs set total=$2, succeeded=$3, failed=$4, results=$5::jsonb,
			lease_expires_at=now() + interval '120 seconds' where id=$1::uuid`,
			id, int64(len(results)), int64(succ), int64(fail), string(payload))
	})
}

func (e *rollingExec) finish(ctx context.Context, id, status string, results []RollingItem) {
	payload, _ := json.Marshal(results)
	succ, fail := tally(results)
	_ = e.svc.dbStore.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update bulk_jobs set status=$2, total=$3, succeeded=$4, failed=$5,
			results=$6::jsonb, finished_at=now(), lease_expires_at=null where id=$1::uuid`,
			id, status, int64(len(results)), int64(succ), int64(fail), string(payload))
	})
}

func (e *rollingExec) status(ctx context.Context, id string) (RollingJob, bool, error) {
	var j RollingJob
	found := false
	err := e.svc.dbStore.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id, status, total, succeeded, failed, created_at, finished_at, results
			from bulk_jobs where id=$1::uuid and kind=$2`, id, bulkKindRollingRestart)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		found = true
		r := res.Rows[0]
		j = RollingJob{
			ID: s0(r[0]), Status: s0(r[1]), Total: int(i64(r[2])),
			Succeeded: int(i64(r[3])), Failed: int(i64(r[4])), CreatedAt: parseTS(s0(r[5])),
		}
		if r[6] != nil {
			t := parseTS(s0(r[6]))
			j.FinishedAt = &t
		}
		if r[7] != nil {
			_ = json.Unmarshal([]byte(*r[7]), &j.Results)
		}
		return nil
	})
	if err != nil {
		return RollingJob{}, false, err
	}
	return j, found, nil
}

func tally(results []RollingItem) (succ, fail int) {
	for _, r := range results {
		if r.Outcome == "restarted" {
			succ++
		} else {
			fail++
		}
	}
	return
}

// --- local helpers (workers stays self-contained) ---

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
