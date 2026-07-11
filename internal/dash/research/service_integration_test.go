//go:build integration

// Live tenant-isolation proof for the Research dashboard read model (gate G1) against a real
// PostgreSQL, over the dashboard dual-GUC RLS seam. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// Assertions run as a NON-superuser role (app_rls) whose Principal carries a role scope.
package research_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/research"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the research dashboard RLS integration test")
	}
	return pg.ParseDSN(d)
}

func tryExec(c *pg.Conn, sql string) { _ = c.Exec(sql) }

func applyFile(t *testing.T, c *pg.Conn, path string) {
	t.Helper()
	ddl, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := c.Exec(string(ddl)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}

func principal(tenantID, role string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "u", Scopes: []string{"role:" + role}})
}

func TestResearchDashboard_TenantIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	tryExec(admin, "drop owned by app_rls cascade")
	tryExec(admin, "drop role if exists app_rls")
	tryExec(admin, "drop table if exists research_runs, research_steps, research_dossiers, research_sources, intent_scores, field_versions, idempotency_ledger, cost_ledger cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")
	applyFile(t, admin, "../../../migrations/0001_init.sql")
	// app_current_role() is the dual-GUC primitive from migration 0004; this minimal chain applies
	// only the R&I migrations, so define the one function the 0017 operator-read policy depends on.
	if err := admin.Exec("CREATE OR REPLACE FUNCTION app_current_role() RETURNS text LANGUAGE sql STABLE AS $f$ SELECT current_setting('app.current_role', true) $f$"); err != nil {
		t.Fatalf("define app_current_role: %v", err)
	}
	// 0017 refines RLS on BOTH R&I read-model tables (research_dossiers + intent_scores), so both
	// table migrations must be present before it — as they are in the production chain.
	applyFile(t, admin, "../../../migrations/0015_research.sql")
	applyFile(t, admin, "../../../migrations/0016_intent.sql")
	applyFile(t, admin, "../../../migrations/0017_ri_operator_read.sql")
	applyFile(t, admin, "../../../migrations/0020_research_runs_operator_read.sql")
	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := admin.Exec("grant select, insert, update, delete on research_runs, research_steps, research_dossiers, research_sources to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := admin.Exec(`insert into research_dossiers (tenant_id, dossier_id, subject_key, dossier, overall_confidence, config_version)
		values ('tenant-A','d-A','acme.com','{"company_profile":{"name":"Acme"}}',0.7,'v1'),
		       ('tenant-B','d-B','other.com','{}',0.5,'v1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := admin.Exec(`insert into research_runs (tenant_id, run_id, subject_key, status, config_version)
		values ('tenant-A','r-A','acme.com','done','v1'),
		       ('tenant-B','r-B','other.com','running','v1')`); err != nil {
		t.Fatalf("seed runs: %v", err)
	}

	appCfg := cfg
	appCfg.User = "app_rls"
	store := db.New(pg.NewPool(appCfg, 4))
	svc := research.NewService(store)

	ctxA := principal("tenant-A", "tenant_admin")
	ctxB := principal("tenant-B", "tenant_admin")

	// tenant-A lists its dossier; tenant-B lists only its own.
	if listA, err := svc.List(ctxA, 50); err != nil || len(listA) != 1 || listA[0].DossierID != "d-A" {
		t.Fatalf("List A = %+v err=%v", listA, err)
	}
	if listB, err := svc.List(ctxB, 50); err != nil || len(listB) != 1 || listB[0].DossierID != "d-B" {
		t.Fatalf("List B = %+v err=%v", listB, err)
	}

	// research_runs monitor: tenant-A sees its run (with status); tenant-B only its own (RLS G1).
	if runsA, err := svc.Runs(ctxA, 50); err != nil || len(runsA) != 1 || runsA[0].RunID != "r-A" || runsA[0].Status != "done" {
		t.Fatalf("Runs A = %+v err=%v", runsA, err)
	}
	if runsB, err := svc.Runs(ctxB, 50); err != nil || len(runsB) != 1 || runsB[0].RunID != "r-B" || runsB[0].Status != "running" {
		t.Fatalf("Runs B = %+v err=%v", runsB, err)
	}

	// tenant-A reads d-A's blob; tenant-B cannot (RLS).
	blob, ok, err := svc.Dossier(ctxA, "d-A")
	if err != nil || !ok || !strings.Contains(string(blob), "Acme") {
		t.Fatalf("Dossier A ok=%v err=%v blob=%s", ok, err, string(blob))
	}
	if _, okB, err := svc.Dossier(ctxB, "d-A"); err != nil || okB {
		t.Fatalf("tenant-B must NOT read tenant-A's dossier (ok=%v err=%v)", okB, err)
	}

	// Operator (dual-GUC role=operator, tenant=platform) reads ACROSS tenants via the additive
	// 0017 operator-read policy (rbac research.read = DecisionAllow). tenant_user stays fail-closed.
	ctxOp := principal("platform", "operator")
	listOp, err := svc.List(ctxOp, 50)
	if err != nil {
		t.Fatalf("List operator: %v", err)
	}
	gotOp := map[string]bool{}
	for _, d := range listOp {
		gotOp[d.DossierID] = true
	}
	if !gotOp["d-A"] || !gotOp["d-B"] {
		t.Fatalf("operator must read across tenants; got %+v", listOp)
	}
	if blobOp, ok, err := svc.Dossier(ctxOp, "d-A"); err != nil || !ok || !strings.Contains(string(blobOp), "Acme") {
		t.Fatalf("operator must read tenant-A's dossier (ok=%v err=%v)", ok, err)
	}
	// Operator reads research RUNS across tenants too, via the additive 0020 operator-read policy.
	runsOp, err := svc.Runs(ctxOp, 50)
	if err != nil {
		t.Fatalf("Runs operator: %v", err)
	}
	gotRuns := map[string]bool{}
	for _, r := range runsOp {
		gotRuns[r.RunID] = true
	}
	if !gotRuns["r-A"] || !gotRuns["r-B"] {
		t.Fatalf("operator must read runs across tenants; got %+v", runsOp)
	}
	// A non-operator role of tenant-B still cannot cross into tenant-A (operator-read is FOR SELECT
	// scoped to the operator role only; tenant_user remains confined by *_tenant_isolation).
	ctxUserB := principal("tenant-B", "tenant_user")
	if _, ok, err := svc.Dossier(ctxUserB, "d-A"); err != nil || ok {
		t.Fatalf("tenant_user of B must NOT read tenant-A's dossier (ok=%v err=%v)", ok, err)
	}

	// Fail-closed: no principal ⇒ error.
	if _, _, err := svc.Dossier(context.Background(), "d-A"); err == nil {
		t.Fatal("service must reject an unauthenticated context")
	}
}
