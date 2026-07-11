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
	tryExec(admin, "drop table if exists research_runs, research_steps, research_dossiers, research_sources, field_versions, idempotency_ledger, cost_ledger cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")
	applyFile(t, admin, "../../../migrations/0001_init.sql")
	applyFile(t, admin, "../../../migrations/0015_research.sql")
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

	// tenant-A reads d-A's blob; tenant-B cannot (RLS).
	blob, ok, err := svc.Dossier(ctxA, "d-A")
	if err != nil || !ok || !strings.Contains(string(blob), "Acme") {
		t.Fatalf("Dossier A ok=%v err=%v blob=%s", ok, err, string(blob))
	}
	if _, okB, err := svc.Dossier(ctxB, "d-A"); err != nil || okB {
		t.Fatalf("tenant-B must NOT read tenant-A's dossier (ok=%v err=%v)", okB, err)
	}

	// Fail-closed: no principal ⇒ error.
	if _, _, err := svc.Dossier(context.Background(), "d-A"); err == nil {
		t.Fatal("service must reject an unauthenticated context")
	}
}
