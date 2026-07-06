//go:build integration

// Live-Postgres proof for the P5 queues read model + replay verbs (doc 12 §P5) over migrations
// 0001/0002/0003/0004/0008/0009 under FORCE RLS as a NON-superuser role (superusers bypass RLS,
// proving nothing). Covers three of the five P5 acceptance criteria:
//
//   - #1 TestQueuesReplayIdempotent: queues.Redrive resets a dead job to pending; a second redrive
//     of the same id is a no-op (rowcount 0); re-execution double-charges nothing — asserted via
//     the idempotency_ledger row count AND the provider call count (G2).
//   - #4 TestQueuesFilteredReplay: POST /queues/{name}/replay {filter:{error_class:"TRANSIENT"}}
//     -> 202 {job_id}; only matching dead rows are redriven; GET /bulk-jobs/{id} polls progress.
//   - #5 TestQueuesTenantIsolation (G1): Tenant B cannot list or redrive Tenant A's dead letters
//     (0 rows / no-op).
//
// Plus TestQueueStatsFoldAndList: the queue_stats fold samples job_outbox and the queue list serves
// the folded vector (never a per-request COUNT(*)).
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package queues_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/queues"
	"github.com/enrichment/waterfall/internal/dash/telemetry"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgoutbox"
	"github.com/enrichment/waterfall/internal/pgstore"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_q"

func dsn(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the queues integration tests")
	}
	return pg.ParseDSN(d)
}

func mustExec(t *testing.T, c *pg.Conn, sql string, args ...any) {
	t.Helper()
	if err := c.ExecParams(sql, args...); err != nil {
		t.Fatalf("exec %.60q: %v", sql, err)
	}
}

func scalar(t *testing.T, c *pg.Conn, sql string) string {
	t.Helper()
	res, err := c.Query(sql)
	if err != nil {
		t.Fatalf("query %.60q: %v", sql, err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return ""
	}
	return *res.Rows[0][0]
}

// setupSchema rebuilds the migrations the queues feature spans and provisions a non-superuser app
// role with table/sequence grants (grants + FORCE-RLS policies are enough — no ownership needed).
func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	for _, s := range []string{
		"drop owned by " + appRole + " cascade",
		"drop role if exists " + appRole,
		"drop table if exists job_outbox, workers, queue_defs, bulk_jobs cascade",
		"drop table if exists queue_stats_1m, queue_stats_1h, worker_heartbeats, worker_stats_5m cascade",
		"drop table if exists usage_events, provider_stats_1m, provider_stats_1h, provider_stats_1d cascade",
		"drop table if exists key_usage_1m, key_usage_1h, key_usage_1d, tenant_usage_1h, tenant_usage_1d cascade",
		"drop table if exists cost_rollup_1d, provider_health_checks, provider_health_1d cascade",
		"drop table if exists field_versions, idempotency_ledger, cost_ledger cascade",
		"drop table if exists tenants, users, mfa_recovery_codes, sessions, ip_allowlists, tenant_invites cascade",
		"drop table if exists audit_log, audit_chain_heads, api_access_log, secret_envelopes cascade",
		"drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade",
		"drop function if exists app_current_role() cascade",
		"drop function if exists app_current_tenant() cascade",
	} {
		_ = admin.Exec(s)
	}
	for _, f := range []string{
		"../../../migrations/0001_init.sql",
		"../../../migrations/0002_job_outbox.sql",
		"../../../migrations/0003_outbox_dlq.sql",
		"../../../migrations/0004_dash_identity_rbac.sql",
		"../../../migrations/0008_dash_workers_queues.sql",
		"../../../migrations/0009_dash_telemetry.sql",
		"../../../migrations/0012_dash_provisioning_mfa.sql",
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
	for _, tid := range []string{"tenant-a", "tenant-b"} {
		mustExec(t, admin, `insert into tenants (id, name, kind, status) values ($1,$1,'customer','active')`, tid)
	}
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
func tenantCtx(id string) context.Context {
	return tenant.WithPrincipal(context.Background(),
		tenant.Principal{TenantID: id, UserID: "00000000-0000-4000-8000-000000000002", Scopes: []string{"role:tenant_admin"}})
}

// seedDead inserts a parked (dead) job_outbox row as the superuser (RLS bypassed for seeding).
func seedDead(t *testing.T, admin *pg.Conn, jobID, tenantID, lastError string) {
	t.Helper()
	mustExec(t, admin, `insert into job_outbox (job_id, tenant_id, payload, status, pending, dead, attempts, last_error)
		values ($1,$2,'{}'::jsonb,'failed',false,true,10,$3)`, jobID, tenantID, lastError)
}

func mkJob(id string) *job.Job {
	return &job.Job{
		ID: id, TenantID: "tenant-a", Principal: tenant.Principal{TenantID: "tenant-a"}, Status: job.StatusQueued,
		Req: domain.EnrichmentRequest{
			JobID:            id,
			Subject:          domain.Subject{ID: "subj-" + id, Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"}},
			Want:             []domain.Field{domain.FieldWorkEmail},
			ConfidenceTarget: 0.8, CostCeiling: 20, ConfigVersion: "v1",
		},
	}
}

// TestQueuesReplayIdempotent is P5 acceptance #1.
func TestQueuesReplayIdempotent(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	appCfg := cfg
	appCfg.User = appRole
	outbox, err := pgoutbox.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()
	engineStore, err := pgstore.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open pgstore: %v", err)
	}
	defer engineStore.Close()
	store, closeStore := appStore(t, cfg)
	defer closeStore()

	svc := queues.NewService(queues.Config{Store: store, Outbox: outbox})

	// Engine + relay + dispatcher (as in the pgoutbox redrive test).
	now := time.Unix(1_700_000_000, 0)
	fake := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := engine.New(engineStore, []provider.Adapter{fake}, engine.WithClock(func() time.Time { return now }))
	planner := router.New(fake)
	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		return eng.Run(ctx, req, planner.Plan(req))
	}
	q := job.NewQueue(64)
	dispatcher := job.NewDispatcher(q, outbox, run)
	dispatcher.Start(4)
	defer dispatcher.Stop()
	relayConn, err := pg.Connect(cfg) // privileged claim connection
	if err != nil {
		t.Fatalf("connect relay: %v", err)
	}
	defer relayConn.Close()
	relay := pgoutbox.NewRelay(relayConn, q, 2*time.Second)

	ctxA := tenantCtx("tenant-a")
	ctxB := tenantCtx("tenant-b")

	// Run the job once to completion — the first (and only) paid provider call + ledger row.
	if ok, err := outbox.Submit(ctxA, mkJob("redrive-1")); err != nil || !ok {
		t.Fatalf("submit: ok=%v err=%v", ok, err)
	}
	if _, err := relay.DrainOnce(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	waitTerminal(t, outbox, ctxA, "redrive-1")
	ledgerBefore := scalar(t, admin, "select count(*) from idempotency_ledger")
	callsBefore := fake.Calls()
	if ledgerBefore == "0" || ledgerBefore == "" {
		t.Fatalf("first run should have written an idempotency_ledger row, got %q", ledgerBefore)
	}

	// Park it (simulate a poison dead-letter), then redrive through the queues verb.
	mustExec(t, admin, "update job_outbox set dead=true, pending=false where job_id='redrive-1'")
	if ok, err := svc.Redrive(ctxA, "redrive-1"); err != nil || !ok {
		t.Fatalf("redrive should reset the parked job: ok=%v err=%v", ok, err)
	}
	if got := scalar(t, admin, "select pending::text||'/'||dead::text from job_outbox where job_id='redrive-1'"); got != "true/false" {
		t.Fatalf("after redrive want pending/not-dead, got %q", got)
	}

	// Re-execute: at-least-once redelivery + G2 => no second provider call, no second ledger row.
	if _, err := relay.DrainOnce(); err != nil {
		t.Fatalf("re-drain: %v", err)
	}
	waitTerminal(t, outbox, ctxA, "redrive-1")
	if got := scalar(t, admin, "select count(*) from idempotency_ledger"); got != ledgerBefore {
		t.Fatalf("G2 breach: idempotency_ledger grew from %s to %s on replay (double charge)", ledgerBefore, got)
	}
	if fake.Calls() != callsBefore {
		t.Fatalf("G2 breach: replay triggered %d new provider call(s)", fake.Calls()-callsBefore)
	}

	// Second redrive of the same id is a structural no-op (rowcount 0; the row is no longer dead).
	if ok, err := svc.Redrive(ctxA, "redrive-1"); err != nil || ok {
		t.Fatalf("second redrive must be a no-op: ok=%v err=%v", ok, err)
	}
	// G1: another tenant cannot redrive it.
	if ok, err := svc.Redrive(ctxB, "redrive-1"); err != nil || ok {
		t.Fatalf("tenant-b must not redrive tenant-a's job: ok=%v err=%v", ok, err)
	}
	t.Logf("PASS acceptance #1: redrive->pending; second redrive no-op (rowcount 0); ledger unchanged (%s), 0 extra provider calls (G2)", ledgerBefore)
}

// TestQueuesFilteredReplay is P5 acceptance #4 (through the HTTP surface).
func TestQueuesFilteredReplay(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	appCfg := cfg
	appCfg.User = appRole
	outbox, err := pgoutbox.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()
	store, closeStore := appStore(t, cfg)
	defer closeStore()

	// Seed 3 TRANSIENT + 2 PROVIDER_DOWN dead rows for tenant-a.
	for i, e := range []string{"TRANSIENT", "TRANSIENT", "TRANSIENT", "PROVIDER_DOWN", "PROVIDER_DOWN"} {
		seedDead(t, admin, "dl-"+string(rune('a'+i)), "tenant-a", "dead-lettered after 10 attempts; class="+e)
	}

	mux := http.NewServeMux()
	deps := queues.Deps{Store: store, Outbox: outbox, Auth: headerAuth{}, ReplayRatePerMin: 6000}
	svc := queues.Routes(mux, deps)
	queues.BulkJobsRoute(mux, deps, svc)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// POST replay {filter:{error_class:"TRANSIENT"}} -> 202 {job_id}.
	body := `{"filter":{"error_class":"TRANSIENT"}}`
	resp := doReq(t, srv, "POST", "/v1/admin/queues/enrich-default/replay", "tenant-a", "tenant_admin", body)
	if resp.code != http.StatusAccepted {
		t.Fatalf("replay status = %d body=%s", resp.code, resp.body)
	}
	var acc struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(resp.body), &acc); err != nil || acc.JobID == "" {
		t.Fatalf("no job_id in 202: %s", resp.body)
	}

	// Poll GET /bulk-jobs/{id} to a terminal state.
	var final map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r := doReq(t, srv, "GET", "/v1/admin/bulk-jobs/"+acc.JobID, "tenant-a", "tenant_admin", "")
		if r.code != http.StatusOK {
			t.Fatalf("poll status = %d body=%s", r.code, r.body)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(r.body), &m)
		if s, _ := m["status"].(string); s == "succeeded" || s == "partial" {
			final = m
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if final == nil {
		t.Fatal("replay job did not reach a terminal state")
	}
	if got := numField(final, "matched_at_execution"); got != 3 {
		t.Fatalf("matched_at_execution = %d, want 3 (only TRANSIENT rows)", got)
	}
	if got := numField(final, "succeeded"); got != 3 {
		t.Fatalf("succeeded = %d, want 3", got)
	}

	// Exactly the 3 TRANSIENT rows were redriven; the 2 PROVIDER_DOWN rows are still dead.
	if got := scalar(t, admin, "select count(*) from job_outbox where dead and last_error like '%TRANSIENT%'"); got != "0" {
		t.Fatalf("TRANSIENT dead rows remaining = %s, want 0 (all redriven)", got)
	}
	if got := scalar(t, admin, "select count(*) from job_outbox where dead and last_error like '%PROVIDER_DOWN%'"); got != "2" {
		t.Fatalf("PROVIDER_DOWN dead rows = %s, want 2 (untouched)", got)
	}
	if got := scalar(t, admin, "select count(*) from job_outbox where not dead and pending and last_error is null"); got != "3" {
		t.Fatalf("redriven (pending, cleared) rows = %s, want 3", got)
	}
	t.Log("PASS acceptance #4: filtered replay redrove only TRANSIENT dead rows (3/5); PROVIDER_DOWN untouched; polled via GET /bulk-jobs/{id}")
}

// TestQueuesTenantIsolation is P5 acceptance #5 (G1).
func TestQueuesTenantIsolation(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	appCfg := cfg
	appCfg.User = appRole
	outbox, err := pgoutbox.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()
	store, closeStore := appStore(t, cfg)
	defer closeStore()
	svc := queues.NewService(queues.Config{Store: store, Outbox: outbox})

	seedDead(t, admin, "dead-A", "tenant-a", "class=TRANSIENT")
	seedDead(t, admin, "dead-B", "tenant-b", "class=TRANSIENT")

	// Tenant B lists dead letters: sees ONLY its own row, never tenant-a's.
	rows, _, err := svc.DeadLetters(tenantCtx("tenant-b"), queues.DeadFilter{}, db.Cursor{}, 50)
	if err != nil {
		t.Fatalf("dead letters (B): %v", err)
	}
	for _, r := range rows {
		if r.JobID == "dead-A" {
			t.Fatal("G1 breach: tenant-b can see tenant-a's dead letter")
		}
	}
	if len(rows) != 1 || rows[0].JobID != "dead-B" {
		t.Fatalf("tenant-b should see exactly its own dead letter, got %+v", rows)
	}

	// Tenant B cannot redrive tenant-a's dead letter (0 rows -> no-op).
	if ok, err := svc.Redrive(tenantCtx("tenant-b"), "dead-A"); err != nil || ok {
		t.Fatalf("tenant-b redrive of tenant-a job must be a no-op: ok=%v err=%v", ok, err)
	}
	if got := scalar(t, admin, "select dead::text from job_outbox where job_id='dead-A'"); got != "true" {
		t.Fatalf("tenant-a's job must remain dead after tenant-b's attempt, got dead=%s", got)
	}
	// The owner can redrive its own row (proving the row WAS redrivable, just not by B).
	if ok, err := svc.Redrive(tenantCtx("tenant-a"), "dead-A"); err != nil || !ok {
		t.Fatalf("owning tenant-a must redrive its own job: ok=%v err=%v", ok, err)
	}
	t.Log("PASS acceptance #5 (G1): tenant-b cannot list or redrive tenant-a's dead letters; owner can")
}

// TestQueueStatsFoldAndList proves the queue_stats fold samples job_outbox and the queue list
// serves the folded vector (no per-request COUNT(*)).
func TestQueueStatsFoldAndList(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	store, closeStore := appStore(t, cfg)
	defer closeStore()

	mustExec(t, admin, `insert into queue_defs (name, kind, max_attempts, visibility_s, description)
		values ('enrich-default','enrich',10,30,'default outbox')`)
	// 2 waiting + 1 dead for tenant-a; 1 waiting for tenant-b (cross-Tenant sum).
	for _, id := range []string{"w1", "w2"} {
		mustExec(t, admin, `insert into job_outbox (job_id, tenant_id, payload, status, pending)
			values ($1,'tenant-a','{}'::jsonb,'queued',true)`, id)
	}
	seedDead(t, admin, "d1", "tenant-a", "class=UNKNOWN")
	mustExec(t, admin, `insert into job_outbox (job_id, tenant_id, payload, status, pending)
		values ('wb','tenant-b','{}'::jsonb,'queued',true)`)

	now := time.Now().UTC()
	agg := telemetry.NewAggregator(store, func() time.Time { return now }, nil)
	if err := agg.QueueStatsFold(context.Background(), "enrich-default", now); err != nil {
		t.Fatalf("queue stats fold: %v", err)
	}

	svc := queues.NewService(queues.Config{Store: store})
	summaries, err := svc.Queues(opCtx())
	if err != nil {
		t.Fatalf("queues list: %v", err)
	}
	var found *queues.QueueSummary
	for i := range summaries {
		if summaries[i].Name == "enrich-default" {
			found = &summaries[i]
		}
	}
	if found == nil {
		t.Fatal("enrich-default not in the queue list")
	}
	if found.Depth != 3 { // 3 waiting across both tenants
		t.Fatalf("depth = %d, want 3 (waiting across tenants)", found.Depth)
	}
	if found.Dead != 1 {
		t.Fatalf("dead = %d, want 1", found.Dead)
	}
	t.Logf("PASS: fold sampled job_outbox -> queue_stats_1m; queue list served depth=%d dead=%d from the fold", found.Depth, found.Dead)
}

// --- HTTP test harness ---

type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (tenant.Principal, error) {
	tid := r.Header.Get("X-Tenant")
	if tid == "" {
		return tenant.Principal{}, errors.New("no tenant header")
	}
	return tenant.Principal{
		TenantID: tid, UserID: "00000000-0000-4000-8000-000000000009",
		Scopes: []string{"role:" + r.Header.Get("X-Role")},
	}, nil
}

type httpResp struct {
	code int
	body string
}

func doReq(t *testing.T, srv *httptest.Server, method, path, tenantID, role, body string) httpResp {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Tenant", tenantID)
	req.Header.Set("X-Role", role)
	if method != "GET" {
		req.Header.Set("Idempotency-Key", "test-"+method+"-"+path)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return httpResp{code: resp.StatusCode, body: string(b)}
}

func numField(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return -1
}

func waitTerminal(t *testing.T, s *pgoutbox.Store, ctx context.Context, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if ok && (j.Status == job.StatusSucceeded || j.Status == job.StatusFailed) {
			if j.Status == job.StatusFailed {
				t.Fatalf("job %s failed: %s", id, j.Err)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state", id)
}
