//go:build integration

// Live-Postgres proof for the P4 telemetry backbone (doc 12 §P4) over migration 0009 under
// FORCE RLS as a NON-superuser role (superusers bypass RLS, proving nothing):
//
//   - Acceptance #1 (TestTelemetryFoldRefoldIdentical): fold 100k synthetic usage_events across
//     several buckets/providers/keys/Tenants -> snapshot every usage-derived rollup -> TRUNCATE
//     -> refold -> byte-identical rows (proves ON CONFLICT replay idempotency). Also proves the
//     incremental additive fold from empty reproduces the same rollups as the repair refold.
//   - Acceptance #5 (TestTelemetryPartitionMaintainer): with an injectable clock, EnsurePartitions
//     creates tomorrow's usage_events partition and DetachExpired detaches expired ones; asserted
//     via the pg_inherits partition catalog.
//   - TestTelemetryReconcileKeyBudgets: the telemetry-backed reconcile rewrites key_budgets.day_used
//     from usage_events ground truth, summed cross-Tenant.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package telemetry_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/telemetry"
	"github.com/enrichment/waterfall/internal/pg"
)

const telRole = "dash_tel"

// telTables are the 0009 partitioned parents the maintainer owns and the fold writes.
var telTables = []string{
	"usage_events", "provider_stats_1m", "provider_stats_1h", "provider_stats_1d",
	"key_usage_1m", "key_usage_1h", "key_usage_1d", "tenant_usage_1h", "tenant_usage_1d",
	"cost_rollup_1d", "queue_stats_1m", "queue_stats_1h", "worker_heartbeats",
	"worker_stats_5m", "provider_health_checks", "provider_health_1d",
}

// rollupTables are the usage-derived rollups snapshotted for the refold-identical assertion.
var rollupTables = []string{
	"provider_stats_1m", "provider_stats_1h", "provider_stats_1d",
	"key_usage_1m", "key_usage_1h", "key_usage_1d",
	"tenant_usage_1h", "tenant_usage_1d", "cost_rollup_1d",
}

// idTables are the 0004 tables the RLS functions + tenants registry need.
var idTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the telemetry integration test")
	}
	return pg.ParseDSN(d)
}

func mustExec(t *testing.T, c *pg.Conn, sql string, args ...any) {
	t.Helper()
	if err := c.ExecParams(sql, args...); err != nil {
		t.Fatalf("exec %q: %v", trunc(sql), err)
	}
}

func tryExec(c *pg.Conn, sql string) { _ = c.Exec(sql) }

func scalar(t *testing.T, c *pg.Conn, sql string) string {
	t.Helper()
	res, err := c.Query(sql)
	if err != nil {
		t.Fatalf("query %q: %v", trunc(sql), err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return ""
	}
	return *res.Rows[0][0]
}

func trunc(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func applyMigration(t *testing.T, admin *pg.Conn, path string) {
	t.Helper()
	ddl, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}

// setupTelemetrySchema rebuilds migrations 0004 + 0009 cleanly, provisions the non-superuser
// telRole as OWNER of the telemetry parents (so it can create/detach partitions and enforce RLS
// as the app role does in production, doc 03 §6), and seeds a few customer Tenants.
func setupTelemetrySchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+telRole+" cascade")
	tryExec(admin, "drop role if exists "+telRole)
	tryExec(admin, "drop table if exists key_budgets cascade")
	tryExec(admin, "drop table if exists "+strings.Join(telTables, ", ")+" cascade")
	tryExec(admin, "drop table if exists "+strings.Join(idTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")

	// app_current_tenant() lives in migration 0001; 0004 and 0009 policies both need it.
	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	applyMigration(t, admin, "../../../migrations/0004_dash_identity_rbac.sql")
	applyMigration(t, admin, "../../../migrations/0009_dash_telemetry.sql")
	// T4/RF-3 (migration 0012, cost portion only — the rest of 0012 needs tables this cost/telemetry
	// schema does not build): widen cost_rollup_1d with key_id so the fold writes the key-scoped grain.
	applyCostKeyID(t, admin)

	mustExec(t, admin, "create role "+telRole+" login nosuperuser")
	for _, tbl := range telTables {
		mustExec(t, admin, "alter table "+tbl+" owner to "+telRole)
	}
	mustExec(t, admin, "grant select on tenants to "+telRole)
	mustExec(t, admin, "grant create on schema public to "+telRole)

	// Customer Tenants (0004 pre-seeds 'platform'); the fold iterates these under RLS.
	for _, tid := range []string{"acme", "globex", "initech"} {
		mustExec(t, admin, `insert into tenants (id, name, kind, status) values ($1,$1,'customer','active')`, tid)
	}
}

// applyCostKeyID applies migration 0012's cost_rollup_1d changes: add key_id to the grain and widen
// the PK to include it. Kept as a focused helper (not a full-0012 apply) because 0012 also touches
// tenants/tenant_invites/bulk_jobs that this schema does not create.
func applyCostKeyID(t *testing.T, admin *pg.Conn) {
	t.Helper()
	mustExec(t, admin, "ALTER TABLE cost_rollup_1d ADD COLUMN key_id text NOT NULL DEFAULT ''")
	mustExec(t, admin, "ALTER TABLE cost_rollup_1d DROP CONSTRAINT cost_rollup_1d_pkey")
	mustExec(t, admin, "ALTER TABLE cost_rollup_1d ADD PRIMARY KEY (tenant_id, provider_id, workflow_key, country, key_id, day)")
}

func appStore(t *testing.T, cfg pg.Config) (*db.Store, func()) {
	t.Helper()
	appCfg := cfg
	appCfg.User = telRole
	pool := pg.NewPool(appCfg, 8)
	return db.New(pool), pool.Close
}

// nEvents is the synthetic feed size for acceptance #1. It is a const so it can be dialed down
// on a slow box; the fold's rollup cardinality is independent of it, so idempotency is proven at
// any size (kept at the documented 100k here).
const nEvents = 100000

// seedUsageEvents inserts nEvents deterministic usage_events (server-side, as the superuser admin
// so RLS is bypassed for seeding) spread across 3 Tenants, 5 Providers, 10 Keys, 3 workflows,
// 3 countries, and 180 one-minute buckets on 2026-07-01.
func seedUsageEvents(t *testing.T, admin *pg.Conn) {
	t.Helper()
	mustExec(t, admin, `insert into usage_events
	  (tenant_id, provider_id, key_id, workflow_key, country, outcome_class, credits, lat_ms, created_at)
	  select
	    (array['acme','globex','initech'])[1 + (g % 3)],
	    (array['hunter','clearbit','apollo','zoominfo','lusha'])[1 + (g % 5)],
	    ('00000000-0000-4000-8000-' || lpad((g % 10)::text, 12, '0'))::uuid,
	    (array['enrich_email','enrich_phone','enrich_company'])[1 + (g % 3)],
	    (array['us','eu','apac'])[1 + (g % 3)],
	    (array['ok','ok','ok','ok','ok','ok','AUTH','RATE_LIMIT','TRANSIENT','NOT_FOUND','BAD_REQUEST','QUOTA','PROVIDER_DOWN','UNKNOWN'])[1 + (g % 14)],
	    (g % 5),
	    (g * 7) % 9000,
	    timestamptz '2026-07-01 00:00:00+00' + ((g % 180) * interval '1 minute')
	  from generate_series(1, `+itoa(nEvents)+`) g`)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// snapshotRollups returns, per rollup table, the md5 of its rows rendered as order-canonical JSON
// plus the row count — a byte-identity fingerprint read as the superuser (bypassing RLS to see
// every Tenant's rows). row_to_json emits columns in table order, so the hash is a faithful
// byte-for-byte fingerprint including lat_hist arrays.
func snapshotRollups(t *testing.T, admin *pg.Conn) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tbl := range rollupTables {
		h := scalar(t, admin,
			`select coalesce(md5(string_agg(row_to_json(x)::text, chr(10) order by row_to_json(x)::text)),'EMPTY')
			   || ':' || count(*)::text from `+tbl+` x`)
		out[tbl] = h
	}
	return out
}

func truncateRollups(t *testing.T, admin *pg.Conn) {
	t.Helper()
	mustExec(t, admin, "truncate "+strings.Join(rollupTables, ", "))
}

// TestTelemetryFoldRefoldIdentical is P4 acceptance #1.
func TestTelemetryFoldRefoldIdentical(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupTelemetrySchema(t, admin)
	seedUsageEvents(t, admin)

	store, closeStore := appStore(t, cfg)
	defer closeStore()

	fixedNow := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	agg := telemetry.NewAggregator(store, func() time.Time { return fixedNow }, nil)
	ctx := context.Background()

	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 1, 4, 0, 0, 0, time.UTC) // bucket-aligned; covers the 180-minute span

	// First refold.
	n1, err := agg.Refold(ctx, from, to)
	if err != nil {
		t.Fatalf("refold #1: %v", err)
	}
	if n1 != nEvents {
		t.Fatalf("refold #1 folded %d events, want %d", n1, nEvents)
	}
	snapA := snapshotRollups(t, admin)
	if ps := scalar(t, admin, "select count(*) from provider_stats_1m"); ps == "0" || ps == "" {
		t.Fatalf("provider_stats_1m empty after fold")
	}
	// T4/RF-3: cost_rollup_1d is folded at the key_id grain. The seed spreads 10 keys, so the fold
	// must produce more than one distinct (non-empty) key_id — proving key_id rides the fold key.
	if nk := scalar(t, admin, "select count(distinct key_id) from cost_rollup_1d where key_id <> ''"); nk == "0" || nk == "1" || nk == "" {
		t.Fatalf("cost_rollup_1d distinct non-empty key_id = %q, want >1 (key_id must be folded into the grain)", nk)
	}

	// Truncate and refold — must reproduce byte-identical rollups.
	truncateRollups(t, admin)
	n2, err := agg.Refold(ctx, from, to)
	if err != nil {
		t.Fatalf("refold #2: %v", err)
	}
	if n2 != nEvents {
		t.Fatalf("refold #2 folded %d events, want %d", n2, nEvents)
	}
	snapB := snapshotRollups(t, admin)
	assertSnapshotsEqual(t, "refold", snapA, snapB)

	// The incremental additive fold from empty reproduces the same rollups as the repair refold.
	truncateRollups(t, admin)
	inc := telemetry.NewAggregator(store, func() time.Time { return fixedNow }, nil)
	inc.SeedWatermark(from)
	nF, err := inc.Fold(ctx)
	if err != nil {
		t.Fatalf("incremental fold: %v", err)
	}
	if nF != nEvents {
		t.Fatalf("incremental fold folded %d events, want %d", nF, nEvents)
	}
	snapC := snapshotRollups(t, admin)
	assertSnapshotsEqual(t, "incremental==refold", snapA, snapC)

	t.Logf("PASS acceptance #1: fold %d usage_events -> snapshot -> truncate -> refold byte-identical (%d rollup tables); incremental additive fold == refold",
		nEvents, len(rollupTables))
}

func assertSnapshotsEqual(t *testing.T, label string, a, b map[string]string) {
	t.Helper()
	for _, tbl := range rollupTables {
		if a[tbl] != b[tbl] {
			t.Fatalf("%s: %s NOT identical:\n  before=%s\n   after=%s", label, tbl, a[tbl], b[tbl])
		}
	}
}

// TestTelemetryPartitionMaintainer is P4 acceptance #5.
func TestTelemetryPartitionMaintainer(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupTelemetrySchema(t, admin) // fresh, no events

	store, closeStore := appStore(t, cfg)
	defer closeStore()

	m := telemetry.NewMaintainer(store, nil, nil)
	ctx := context.Background()

	past := time.Date(2020, 1, 10, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	// Pre-create expired partitions (offsets 0..2 => 2020-01-10/11/12).
	if err := m.EnsurePartitions(ctx, past); err != nil {
		t.Fatalf("ensure(past): %v", err)
	}
	if !partitionExists(t, admin, "usage_events", "usage_events_p20200110") {
		t.Fatalf("expected expired partition usage_events_p20200110 to be created")
	}

	// Ensure current + ahead partitions (offsets 0..2 => 2026-07-02/03/04).
	if err := m.EnsurePartitions(ctx, now); err != nil {
		t.Fatalf("ensure(now): %v", err)
	}
	if !partitionExists(t, admin, "usage_events", "usage_events_p20260703") {
		t.Fatalf("expected tomorrow's partition usage_events_p20260703 to be created")
	}

	// Detach expired: usage_events retention 48h -> the 2020 partitions are past cutoff.
	removed, err := m.DetachExpired(ctx, now)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if partitionExists(t, admin, "usage_events", "usage_events_p20200110") {
		t.Fatalf("expired partition usage_events_p20200110 was not detached")
	}
	if !partitionExists(t, admin, "usage_events", "usage_events_p20260703") {
		t.Fatalf("in-retention partition usage_events_p20260703 was wrongly detached")
	}
	// The _default backstop is never detached.
	if !partitionExists(t, admin, "usage_events", "usage_events_default") {
		t.Fatalf("usage_events_default backstop was detached")
	}
	if len(removed) == 0 {
		t.Fatalf("DetachExpired reported nothing removed")
	}

	t.Logf("PASS acceptance #5: EnsurePartitions created tomorrow's usage_events partition; DetachExpired removed %d expired partitions (default backstop preserved)",
		len(removed))
}

func partitionExists(t *testing.T, admin *pg.Conn, parent, child string) bool {
	t.Helper()
	n := scalar(t, admin,
		`select count(*) from pg_inherits i
		   join pg_class c on c.oid = i.inhrelid
		   join pg_class p on p.oid = i.inhparent
		  where p.relname = '`+parent+`' and c.relname = '`+child+`'`)
	return n != "0" && n != ""
}

// TestTelemetryLeaderElection proves the aggregator advisory-lock leader election is mutually
// exclusive: while one instance holds hashtext('dash_aggregator'), a second acquire fails; after
// release, it succeeds again (doc 10 §2 single-writer election).
func TestTelemetryLeaderElection(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupTelemetrySchema(t, admin)

	appCfg := cfg
	appCfg.User = telRole
	pool := pg.NewPool(appCfg, 4)
	defer pool.Close()
	ctx := context.Background()

	lead1, ok1, err := telemetry.TryAcquireLeadership(ctx, pool)
	if err != nil || !ok1 {
		t.Fatalf("first acquire = (ok=%v, err=%v), want leader", ok1, err)
	}
	_, ok2, err := telemetry.TryAcquireLeadership(ctx, pool)
	if err != nil {
		t.Fatalf("second acquire errored: %v", err)
	}
	if ok2 {
		t.Fatalf("second acquire became leader while first holds the lock (not mutually exclusive)")
	}
	lead1.Release()
	lead3, ok3, err := telemetry.TryAcquireLeadership(ctx, pool)
	if err != nil || !ok3 {
		t.Fatalf("re-acquire after release = (ok=%v, err=%v), want leader", ok3, err)
	}
	lead3.Release()
	t.Logf("PASS: aggregator leadership is mutually exclusive (advisory lock dash_aggregator); released and re-acquired")
}

// TestTelemetryReconcileKeyBudgets proves the telemetry-backed key-budget reconcile rewrites
// day_used from usage_events ground truth, summed across Tenants.
func TestTelemetryReconcileKeyBudgets(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupTelemetrySchema(t, admin)

	// Minimal key_budgets (rotation owns the real one in 0005; reconcile only touches these
	// columns). Class P (platform-only), owned by the app role so PlatformTx can UPDATE it.
	mustExec(t, admin, `create table key_budgets (
		key_id uuid primary key, day date not null default current_date,
		day_used bigint not null default 0, day_leased bigint not null default 0,
		updated_at timestamptz not null default now())`)
	mustExec(t, admin, "alter table key_budgets enable row level security")
	mustExec(t, admin, "alter table key_budgets force row level security")
	mustExec(t, admin, `create policy key_budgets_platform_only on key_budgets
		using (app_current_tenant() = 'platform') with check (app_current_tenant() = 'platform')`)
	mustExec(t, admin, "alter table key_budgets owner to "+telRole)

	k1 := "00000000-0000-4000-8000-000000000001"
	k2 := "00000000-0000-4000-8000-000000000002"
	mustExec(t, admin, `insert into key_budgets (key_id, day, day_used) values ($1::uuid, date '2026-07-01', 0)`, k1)
	mustExec(t, admin, `insert into key_budgets (key_id, day, day_used) values ($1::uuid, date '2026-07-01', 0)`, k2)

	// Ground truth on 2026-07-01: k1 = 20 (acme) + 10 (globex) = 30 cross-Tenant; k2 = 10 (acme).
	ins := func(tenantID, keyID string, credits int64) {
		mustExec(t, admin, `insert into usage_events (tenant_id, provider_id, key_id, outcome_class, credits, lat_ms, created_at)
			values ($1,'hunter',$2::uuid,'ok',$3, 100, timestamptz '2026-07-01 09:00:00+00')`, tenantID, keyID, credits)
	}
	ins("acme", k1, 10)
	ins("acme", k1, 10)
	ins("globex", k1, 10)
	ins("acme", k2, 4)
	ins("acme", k2, 6)

	store, closeStore := appStore(t, cfg)
	defer closeStore()

	rec := telemetry.NewReconciler(store)
	n, err := rec.ReconcileKeyBudgets(context.Background(), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("reconcile rewrote %d keys, want 2", n)
	}
	if got := scalar(t, admin, "select day_used from key_budgets where key_id = '"+k1+"'"); got != "30" {
		t.Fatalf("k1 day_used = %q, want 30 (cross-Tenant sum)", got)
	}
	if got := scalar(t, admin, "select day_used from key_budgets where key_id = '"+k2+"'"); got != "10" {
		t.Fatalf("k2 day_used = %q, want 10", got)
	}
	t.Logf("PASS: reconcile rewrote key_budgets.day_used from usage_events ground truth (k1=30 cross-Tenant, k2=10)")
}
