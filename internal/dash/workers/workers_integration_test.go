//go:build integration

// Live-Postgres proof for the P5 worker registry + convergence (doc 12 §P5) over migrations
// 0001/0004/0008/0009 under FORCE RLS as a NON-superuser role. Covers two P5 acceptance criteria
// with an injectable clock:
//
//   - #2 TestWorkersDrainConverges: a worker with 3 in-flight jobs receives desired_state=draining
//     via the heartbeat channel; claims stop immediately; the workers row reaches status=stopped
//     ONLY when jobs_active=0; no job is abandoned.
//   - #3 TestWorkersLostDetection: with the injectable clock, a worker whose heartbeats stop is
//     marked lost after the 3-interval staleness threshold (2-pass hysteresis); a resumed heartbeat
//     restores running.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package workers_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/workers"
	"github.com/enrichment/waterfall/internal/heartbeat"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_w"

func dsn(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the workers integration tests")
	}
	return pg.ParseDSN(d)
}

func mustExec(t *testing.T, c *pg.Conn, sql string, args ...any) {
	t.Helper()
	if err := c.ExecParams(sql, args...); err != nil {
		t.Fatalf("exec %.60q: %v", sql, err)
	}
}

func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	for _, s := range []string{
		"drop owned by " + appRole + " cascade",
		"drop role if exists " + appRole,
		"drop table if exists workers, queue_defs, bulk_jobs cascade",
		"drop table if exists queue_stats_1m, queue_stats_1h, worker_heartbeats, worker_stats_5m cascade",
		"drop table if exists usage_events, provider_stats_1m, provider_stats_1h, provider_stats_1d cascade",
		"drop table if exists key_usage_1m, key_usage_1h, key_usage_1d, tenant_usage_1h, tenant_usage_1d cascade",
		"drop table if exists cost_rollup_1d, provider_health_checks, provider_health_1d cascade",
		"drop table if exists field_versions, idempotency_ledger, cost_ledger cascade",
		"drop table if exists tenants, users, mfa_recovery_codes, sessions, ip_allowlists cascade",
		"drop table if exists audit_log, audit_chain_heads, api_access_log, secret_envelopes cascade",
		"drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade",
		"drop function if exists app_current_role() cascade",
		"drop function if exists app_current_tenant() cascade",
	} {
		_ = admin.Exec(s)
	}
	for _, f := range []string{
		"../../../migrations/0001_init.sql",
		"../../../migrations/0004_dash_identity_rbac.sql",
		"../../../migrations/0008_dash_workers_queues.sql",
		"../../../migrations/0009_dash_telemetry.sql",
	} {
		ddl, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := admin.Exec(string(ddl)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on all tables in schema public to "+appRole)
	mustExec(t, admin, "grant usage, select on all sequences in schema public to "+appRole)
}

func appStore(t *testing.T, cfg pg.Config) (*db.Store, func()) {
	t.Helper()
	c := cfg
	c.User = appRole
	pool := pg.NewPool(c, 8)
	return db.New(pool), pool.Close
}

func opCtx() context.Context {
	return tenant.WithPrincipal(context.Background(),
		tenant.Principal{TenantID: "platform", UserID: "00000000-0000-4000-8000-000000000001", Scopes: []string{"role:operator"}})
}

// storeTransport is the heartbeat client's transport over the real registry Store: it upserts the
// beat and echoes the desired_state, exactly like the HTTP heartbeat handler.
type storeTransport struct {
	store *workers.Store
	ctx   context.Context
	now   func() time.Time
}

func (s storeTransport) Send(_ context.Context, b heartbeat.Beat) (heartbeat.Ack, error) {
	row, err := s.store.Upsert(s.ctx, workers.Beat{
		ID: b.WorkerID, Status: b.Status, JobsActive: b.JobsActive, JobsDone: b.JobsDone,
		Queue: b.Queue, Kind: b.Kind, Region: b.Region, Version: b.Version,
	}, s.now())
	if err != nil {
		return heartbeat.Ack{}, err
	}
	return heartbeat.Ack{DesiredState: row.DesiredState}, nil
}

// TestWorkersDrainConverges is P5 acceptance #2.
func TestWorkersDrainConverges(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	dbStore, closeStore := appStore(t, cfg)
	defer closeStore()

	store := workers.NewStore(dbStore)
	op := opCtx()
	clk := &clock{t: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)}
	c := heartbeat.New(heartbeat.Config{
		Transport: storeTransport{store: store, ctx: op, now: clk.now}, WorkerID: "w-enrich-7", Now: clk.now,
	})
	ctx := context.Background()

	// Register with 3 in-flight jobs.
	c.SetJobsActive(3)
	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("register beat: %v", err)
	}
	if !c.ShouldClaim() {
		t.Fatal("a running worker must claim")
	}

	// Operator drains.
	if _, err := store.SetDesiredState(op, "w-enrich-7", workers.DesiredDraining, false, clk.now()); err != nil {
		t.Fatalf("set draining: %v", err)
	}
	if _, err := c.Beat(ctx); err != nil { // learns draining
		t.Fatalf("drain-learn beat: %v", err)
	}
	if c.ShouldClaim() {
		t.Fatal("claims must stop IMMEDIATELY once draining is echoed")
	}

	// Converge: finish the 3 jobs; the row must never read stopped while jobs_active > 0.
	for i := 3; i > 0; i-- {
		if _, err := c.Beat(ctx); err != nil {
			t.Fatalf("drain beat: %v", err)
		}
		row := getWorker(t, store, op, "w-enrich-7")
		if row.JobsActive > 0 && row.Status == workers.StatusStopped {
			t.Fatalf("status reached stopped with %d jobs still in flight — a job was abandoned", row.JobsActive)
		}
		c.FinishJob()
	}
	if _, err := c.Beat(ctx); err != nil { // jobs_active now 0 -> stopped
		t.Fatalf("final beat: %v", err)
	}
	row := getWorker(t, store, op, "w-enrich-7")
	if row.Status != workers.StatusStopped || row.JobsActive != 0 || row.DesiredState != workers.DesiredDraining {
		t.Fatalf("drain must converge to stopped@0 under desired=draining, got status=%s active=%d desired=%s",
			row.Status, row.JobsActive, row.DesiredState)
	}
	t.Log("PASS acceptance #2: drain stops claiming immediately; workers row reaches stopped only at jobs_active=0; no job abandoned")
}

// TestWorkersLostDetection is P5 acceptance #3.
func TestWorkersLostDetection(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	dbStore, closeStore := appStore(t, cfg)
	defer closeStore()

	store := workers.NewStore(dbStore)
	op := opCtx()
	t0 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	// Register a worker beating at t0.
	if _, err := store.Upsert(op, workers.Beat{ID: "w-lost", Status: workers.StatusRunning}, t0); err != nil {
		t.Fatalf("register: %v", err)
	}
	det := workers.NewDetector(workers.DetectorConfig{
		Store: store, Interval: 10 * time.Second, MissIntervals: 3, Hysteresis: 2,
	})

	// Within 3 intervals: not overdue, never lost.
	passExpect(t, det, op, t0.Add(25*time.Second), nil)
	if getWorker(t, store, op, "w-lost").Status == workers.StatusLost {
		t.Fatal("marked lost inside the 3-interval threshold")
	}
	// Past the staleness threshold: first pass strikes, hysteresis withholds lost.
	passExpect(t, det, op, t0.Add(31*time.Second), nil)
	if getWorker(t, store, op, "w-lost").Status == workers.StatusLost {
		t.Fatal("hysteresis: a single overdue pass must not mark lost")
	}
	// Second consecutive overdue pass: lost.
	passExpect(t, det, op, t0.Add(41*time.Second), []string{"w-lost"})
	if getWorker(t, store, op, "w-lost").Status != workers.StatusLost {
		t.Fatal("w-lost must be lost after the hysteresis is satisfied past 3 intervals")
	}

	// Resume: a fresh heartbeat re-adopts the row to running; no further alert.
	if _, err := store.Upsert(op, workers.Beat{ID: "w-lost", Status: workers.StatusRunning}, t0.Add(45*time.Second)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	passExpect(t, det, op, t0.Add(50*time.Second), nil)
	if got := getWorker(t, store, op, "w-lost").Status; got != workers.StatusRunning {
		t.Fatalf("resumed worker must be running, got %q", got)
	}
	t.Log("PASS acceptance #3: lost after 3 missed intervals (2-pass hysteresis); resumed heartbeat restores running")
}

// --- helpers ---

func getWorker(t *testing.T, store *workers.Store, ctx context.Context, id string) workers.WorkerRow {
	t.Helper()
	row, ok, err := store.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", id, ok, err)
	}
	return row
}

func passExpect(t *testing.T, det *workers.Detector, ctx context.Context, now time.Time, want []string) {
	t.Helper()
	got, err := det.Pass(ctx, now)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(want) != len(got) {
		t.Fatalf("pass at %v: lost=%v, want %v", now, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pass at %v: lost=%v, want %v", now, got, want)
		}
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
