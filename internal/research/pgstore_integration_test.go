//go:build integration

// Live tenant-isolation (RLS) proof for the research store — the docs/21 §1 release-blocker
// pattern (gate G1) against a real PostgreSQL. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// All isolation assertions run as a NON-superuser role (app_rls); superusers/BYPASSRLS roles are
// exempt from row-level security, so testing as one would prove nothing.
package research_test

import (
	"context"
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/research"
	"github.com/enrichment/waterfall/internal/tenant"
)

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the research RLS integration test")
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

func setupResearchSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by app_rls cascade")
	tryExec(admin, "drop role if exists app_rls")
	tryExec(admin, "drop table if exists research_runs, research_steps, research_dossiers, research_sources, field_versions, idempotency_ledger, cost_ledger cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")
	applyFile(t, admin, "../../migrations/0001_init.sql") // provides app_current_tenant()
	applyFile(t, admin, "../../migrations/0015_research.sql")
	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := admin.Exec("grant select, insert, update, delete on research_runs, research_steps, research_dossiers, research_sources to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func TestResearchRLS_DossierIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupResearchSchema(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls" // the role RLS actually applies to
	store, err := research.OpenStore(appCfg, 4)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctxA := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A"})
	ctxB := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-B"})

	d := research.Dossier{
		DossierID:      "d-A",
		Subject:        research.Subject{Domain: "acme.com"},
		CompanyProfile: map[string]string{"name": "Acme"},
		Firmographics:  map[string]string{"company_name": "Acme"},
		Confidence:     research.ConfidenceSection{Overall: 0.7},
		Provenance: []research.Source{
			{Field: "company_name", Provider: "brandfetch", SourceType: research.SourceAPI, Confidence: 0.8},
			{Field: "ai_summary", Provider: "llama:free", SourceType: research.SourceAI, Confidence: 0.4},
		},
	}
	if err := store.SaveDossier(ctxA, "d-A", "acme.com", d); err != nil {
		t.Fatalf("save under A: %v", err)
	}

	// tenant-A reads its own dossier.
	got, ok, err := store.GetDossier(ctxA, "d-A")
	if err != nil || !ok {
		t.Fatalf("get under A: ok=%v err=%v", ok, err)
	}
	if got.CompanyProfile["name"] != "Acme" {
		t.Fatalf("A dossier = %+v", got)
	}
	// tenant-B cannot see tenant-A's dossier by id (RLS SELECT isolation, G1).
	if _, okB, err := store.GetDossier(ctxB, "d-A"); err != nil || okB {
		t.Fatalf("tenant-B must NOT see tenant-A's dossier by id (ok=%v err=%v)", okB, err)
	}
	// ...nor by subject.
	if _, okSub, err := store.LatestBySubject(ctxB, "acme.com"); err != nil || okSub {
		t.Fatalf("tenant-B must NOT see tenant-A's dossier by subject (ok=%v err=%v)", okSub, err)
	}
	// tenant-A's LatestBySubject finds it.
	if _, okA, err := store.LatestBySubject(ctxA, "acme.com"); err != nil || !okA {
		t.Fatalf("tenant-A should find its dossier by subject (ok=%v err=%v)", okA, err)
	}

	// Fail-closed: no principal ⇒ error, never a cross-tenant read.
	if _, _, err := store.GetDossier(context.Background(), "d-A"); err == nil {
		t.Fatal("store must reject an unauthenticated context")
	}

	// Cross-tenant WITH CHECK: a raw insert of a tenant-A row while the GUC is tenant-B is rejected.
	raw, err := pg.Connect(appCfg)
	if err != nil {
		t.Fatalf("connect app_rls: %v", err)
	}
	defer raw.Close()
	_ = raw.Exec("set app.current_tenant = 'tenant-B'")
	if err := raw.Exec(`insert into research_dossiers (tenant_id, dossier_id, subject_key, dossier) values ('tenant-A','evil','x','{}')`); err == nil {
		t.Fatal("RLS WITH CHECK must reject a cross-tenant INSERT")
	}
}
