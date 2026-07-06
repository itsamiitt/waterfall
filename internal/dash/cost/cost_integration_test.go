//go:build integration

// Live-Postgres proof for the P6 cost-analytics surface (module 10, doc 12 §P6) over migrations
// 0004 + 0006 (budgets) + 0009 (rollups) + 0012's cost_rollup_1d.key_id (T4/RF-3) + a local
// cost_ledger, under FORCE ROW LEVEL SECURITY as a NON-superuser role (superusers bypass RLS,
// proving nothing):
//
//   - TestCostGroupBysMatchLedgers (P6 gate #2): SUM(credits) from cost/summary equals the
//     cost_ledger committed total for the same window, per Tenant (group_by=tenant) and summed
//     across Providers (group_by=provider) — with consistently seeded cost_rollup_1d + cost_ledger.
//   - TestCostRLSIsolation (P6 gate #5): a Tenant's cost query never sees another Tenant's rollup
//     rows; budgets are Tenant-isolated; group_by=key (now served from the Tenant-isolated
//     cost_rollup_1d.key_id, T4/RF-3) drills into a Tenant's OWN keys and never another's.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package cost_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/cost"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const costRole = "dash_cost"

var costTables = []string{
	"cost_rollup_1d", "tenant_usage_1h", "tenant_usage_1d", "key_usage_1m", "key_usage_1h",
	"key_usage_1d", "usage_events", "provider_stats_1m", "provider_stats_1h", "provider_stats_1d",
	"queue_stats_1m", "queue_stats_1h", "worker_heartbeats", "worker_stats_5m",
	"provider_health_checks", "provider_health_1d",
}
var costCfgTables = []string{"config_versions", "config_active", "config_epochs", "workflow_index", "budgets"}
var costIDTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the cost integration test")
	}
	return pg.ParseDSN(d)
}

func mustExec(t *testing.T, c *pg.Conn, sql string, args ...any) {
	t.Helper()
	if err := c.ExecParams(sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func tryExec(c *pg.Conn, sql string) { _ = c.Exec(sql) }

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

func setupCostSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+costRole+" cascade")
	tryExec(admin, "drop role if exists "+costRole)
	tryExec(admin, "drop table if exists cost_ledger cascade")
	tryExec(admin, "drop table if exists key_budgets cascade")
	tryExec(admin, "drop table if exists "+strings.Join(costTables, ", ")+" cascade")
	tryExec(admin, "drop table if exists "+strings.Join(costCfgTables, ", ")+" cascade")
	tryExec(admin, "drop table if exists "+strings.Join(costIDTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")

	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)
	applyMigration(t, admin, "../../../migrations/0004_dash_identity_rbac.sql")
	applyMigration(t, admin, "../../../migrations/0006_dash_config_versions.sql")
	applyMigration(t, admin, "../../../migrations/0009_dash_telemetry.sql")
	// T4/RF-3 (migration 0012, cost portion): add key_id to cost_rollup_1d so group_by=key serves it.
	mustExec(t, admin, "ALTER TABLE cost_rollup_1d ADD COLUMN key_id text NOT NULL DEFAULT ''")
	mustExec(t, admin, "ALTER TABLE cost_rollup_1d DROP CONSTRAINT cost_rollup_1d_pkey")
	mustExec(t, admin, "ALTER TABLE cost_rollup_1d ADD PRIMARY KEY (tenant_id, provider_id, workflow_key, country, key_id, day)")

	// cost_ledger (migration 0001) — the G4 committed-spend ledger the P6 reconciliation compares to.
	mustExec(t, admin, `create table cost_ledger (
		tenant_id text not null, job_id text not null,
		committed bigint not null default 0 check (committed >= 0),
		primary key (tenant_id, job_id))`)
	mustExec(t, admin, "alter table cost_ledger enable row level security")
	mustExec(t, admin, "alter table cost_ledger force row level security")
	mustExec(t, admin, `create policy cost_ledger_tenant_isolation on cost_ledger
		using (tenant_id = app_current_tenant()) with check (tenant_id = app_current_tenant())`)

	mustExec(t, admin, "create role "+costRole+" login nosuperuser")
	grantTables := append(append(append([]string{}, costTables...), costCfgTables...), costIDTables...)
	grantTables = append(grantTables, "cost_ledger")
	mustExec(t, admin, "grant select, insert, update, delete on "+strings.Join(grantTables, ", ")+" to "+costRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+costRole)

	for _, tid := range []string{"acme", "globex"} {
		mustExec(t, admin, `insert into tenants (id, name, kind, status) values ($1,$1,'customer','active')`, tid)
	}
}

func appStore(t *testing.T, cfg pg.Config) *db.Store {
	appCfg := cfg
	appCfg.User = costRole
	pool := pg.NewPool(appCfg, 8)
	t.Cleanup(pool.Close)
	return db.New(pool)
}

func ctxFor(tid, role string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: tid, UserID: "u-" + tid, Scopes: []string{"role:" + role},
	})
}

// seedCostData writes consistent cost_rollup_1d + cost_ledger totals: acme=500 (hunter 300 +
// clearbit 200), globex=700 (hunter). Ledger committed totals mirror the modeled rollup totals.
// Since T4/RF-3, cost_rollup_1d carries key_id: acme's spend is split across two keys (k-acme-h=300,
// k-acme-c=200), globex's across one (k-globex-h=700), so group_by=key drills down per key.
func seedCostData(t *testing.T, admin *pg.Conn) {
	t.Helper()
	rows := []struct {
		tenant, provider, workflow, country, key string
		credits, calls, success                  int64
	}{
		{"acme", "hunter", "email", "us", "k-acme-h", 300, 320, 300},
		{"acme", "clearbit", "email", "us", "k-acme-c", 200, 210, 190},
		{"globex", "hunter", "phone", "eu", "k-globex-h", 700, 720, 690},
	}
	for _, r := range rows {
		mustExec(t, admin, `insert into cost_rollup_1d
			(tenant_id, provider_id, workflow_key, country, key_id, day, credits, calls, successful_results)
			values ($1,$2,$3,$4,$5, date '2026-07-01', $6,$7,$8)`,
			r.tenant, r.provider, r.workflow, r.country, r.key, r.credits, r.calls, r.success)
	}
	// cost_ledger committed totals per Tenant (two jobs for acme summing to 500; one for globex).
	mustExec(t, admin, `insert into cost_ledger (tenant_id, job_id, committed) values
		('acme','j1',300), ('acme','j2',200), ('globex','j3',700)`)
}

func fixedNow() time.Time { return time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC) }

func window() (time.Time, time.Time) {
	return time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
}

func sumCredits(rows []cost.Row) int64 {
	var s int64
	for _, r := range rows {
		s += r.Credits
	}
	return s
}

func ledgerTotal(t *testing.T, store *db.Store, tid string) int64 {
	var total int64
	err := store.Tx(ctxFor(tid, "tenant_admin"), func(c *pg.Conn) error {
		res, err := c.Query("select coalesce(sum(committed),0) from cost_ledger")
		if err != nil {
			return err
		}
		if res.Rows[0][0] != nil {
			total, _ = strconv.ParseInt(*res.Rows[0][0], 10, 64)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ledger total: %v", err)
	}
	return total
}

// TestCostGroupBysMatchLedgers is P6 gate #2.
func TestCostGroupBysMatchLedgers(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupCostSchema(t, admin)
	seedCostData(t, admin)

	store := appStore(t, cfg)
	svc := cost.NewService(store, fixedNow)
	from, to := window()

	for _, tid := range []string{"acme", "globex"} {
		ctx := ctxFor(tid, "tenant_admin")
		byTenant, _, err := svc.Summary(ctx, "tenant", from, to, nil, false, db.Cursor{}, 0)
		if err != nil {
			t.Fatalf("%s group_by=tenant: %v", tid, err)
		}
		byProvider, _, err := svc.Summary(ctx, "provider", from, to, nil, false, db.Cursor{}, 0)
		if err != nil {
			t.Fatalf("%s group_by=provider: %v", tid, err)
		}
		want := ledgerTotal(t, store, tid)
		if got := sumCredits(byTenant); got != want {
			t.Fatalf("%s: group_by=tenant SUM(credits)=%d, cost_ledger total=%d", tid, got, want)
		}
		if got := sumCredits(byProvider); got != want {
			t.Fatalf("%s: group_by=provider SUM(credits)=%d, cost_ledger total=%d", tid, got, want)
		}
	}
}

// TestCostRLSIsolation is P6 gate #5 (cost half).
func TestCostRLSIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupCostSchema(t, admin)
	seedCostData(t, admin)

	store := appStore(t, cfg)
	svc := cost.NewService(store, fixedNow)
	from, to := window()

	// acme's tenant-grouped read must see only acme (never globex's 700).
	rows, _, err := svc.Summary(ctxFor("acme", "tenant_admin"), "tenant", from, to, nil, false, db.Cursor{}, 0)
	if err != nil {
		t.Fatalf("acme summary: %v", err)
	}
	for _, r := range rows {
		if r.Key == "globex" {
			t.Fatalf("acme saw globex rollup rows: %+v", rows)
		}
	}
	if got := sumCredits(rows); got != 500 {
		t.Fatalf("acme total=%d, want 500 (globex must be invisible)", got)
	}

	// Budgets are Tenant-isolated: write acme's budget, then globex must not see it.
	if _, err := svc.ReplaceBudgets(ctxFor("acme", "tenant_admin"),
		[]cost.Budget{{Scope: "tenant", ScopeKey: "acme", Period: "month", LimitCredits: 1000000, AlertPct: []int{80}}}); err != nil {
		t.Fatalf("acme put budget: %v", err)
	}
	gb, err := svc.ListBudgets(ctxFor("globex", "tenant_admin"))
	if err != nil {
		t.Fatalf("globex list budgets: %v", err)
	}
	if len(gb) != 0 {
		t.Fatalf("globex saw acme's budgets: %+v", gb)
	}

	// group_by=key now serves cost_rollup_1d (T4/RF-3): tenant-scoped by RLS, so a Tenant drills
	// into its OWN keys and never sees another Tenant's. acme has two keys summing to its ledger
	// total (500); globex's key must be invisible.
	keyRows, _, err := svc.Summary(ctxFor("acme", "tenant_admin"), "key", from, to, nil, false, db.Cursor{}, 0)
	if err != nil {
		t.Fatalf("acme group_by=key: %v", err)
	}
	perKey := map[string]int64{}
	for _, r := range keyRows {
		if r.Key == "k-globex-h" {
			t.Fatalf("acme saw globex's key row: %+v", keyRows)
		}
		perKey[r.Key] = r.Credits
	}
	if perKey["k-acme-h"] != 300 || perKey["k-acme-c"] != 200 {
		t.Fatalf("acme per-key credits = %v, want k-acme-h=300 k-acme-c=200", perKey)
	}
	if got := sumCredits(keyRows); got != 500 {
		t.Fatalf("acme group_by=key total=%d, want 500 (== ledger)", got)
	}
}
