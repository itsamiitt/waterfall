//go:build integration

// Live tenant-isolation (RLS) proof for the intent store — the docs/21 §1 release-blocker pattern
// (gate G1) against a real PostgreSQL. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// All isolation assertions run as a NON-superuser role (app_rls); superusers/BYPASSRLS roles are
// exempt from row-level security, so testing as one would prove nothing.
package intent_test

import (
	"context"
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/intent"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the intent RLS integration test")
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

func setupIntentSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by app_rls cascade")
	tryExec(admin, "drop role if exists app_rls")
	tryExec(admin, "drop table if exists intent_scores, field_versions, idempotency_ledger, cost_ledger cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")
	applyFile(t, admin, "../../migrations/0001_init.sql") // provides app_current_tenant()
	applyFile(t, admin, "../../migrations/0016_intent.sql")
	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := admin.Exec("grant select, insert, update, delete on intent_scores to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func TestIntentRLS_ScoreIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupIntentSchema(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls"
	store, err := intent.OpenStore(appCfg, 4)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctxA := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A"})
	ctxB := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-B"})

	scores := []intent.ClassScore{
		{Class: intent.ClassHiring, Score: 0.8, Confidence: 0.7, SignalCount: 2,
			Reasoning: []intent.Contribution{{Type: "eng_hiring", Provider: "theirstack", Weighted: 0.8}}},
		{Class: intent.ClassBuying, Score: 0.6, Confidence: 0.5, SignalCount: 1},
	}
	if err := store.SaveScores(ctxA, "acme.com", "iw-v1", scores); err != nil {
		t.Fatalf("save under A: %v", err)
	}

	// tenant-A reads its scores (score desc), with reasoning preserved.
	got, err := store.GetByAccount(ctxA, "acme.com")
	if err != nil {
		t.Fatalf("get under A: %v", err)
	}
	if len(got) != 2 || got[0].Class != intent.ClassHiring || got[0].Score != 0.8 {
		t.Fatalf("A scores = %+v", got)
	}
	if len(got[0].Reasoning) != 1 || got[0].Reasoning[0].Type != "eng_hiring" {
		t.Fatalf("reasoning not round-tripped: %+v", got[0].Reasoning)
	}

	// tenant-B sees none of tenant-A's scores (RLS SELECT isolation, G1).
	if gotB, err := store.GetByAccount(ctxB, "acme.com"); err != nil || len(gotB) != 0 {
		t.Fatalf("tenant-B must NOT see tenant-A's scores (n=%d err=%v)", len(gotB), err)
	}

	// Upsert-per-(account,class): re-saving updates in place (no duplicate rows).
	scores[0].Score = 0.9
	if err := store.SaveScores(ctxA, "acme.com", "iw-v1", scores); err != nil {
		t.Fatalf("re-save under A: %v", err)
	}
	if got2, err := store.GetByAccount(ctxA, "acme.com"); err != nil || len(got2) != 2 || got2[0].Score != 0.9 {
		t.Fatalf("upsert failed: n=%d err=%v scores=%+v", len(got2), err, got2)
	}

	// Fail-closed: no principal ⇒ error.
	if _, err := store.GetByAccount(context.Background(), "acme.com"); err == nil {
		t.Fatal("store must reject an unauthenticated context")
	}

	// Cross-tenant WITH CHECK: a raw insert of a tenant-A row while GUC=tenant-B is rejected.
	raw, err := pg.Connect(appCfg)
	if err != nil {
		t.Fatalf("connect app_rls: %v", err)
	}
	defer raw.Close()
	_ = raw.Exec("set app.current_tenant = 'tenant-B'")
	if err := raw.Exec(`insert into intent_scores (tenant_id, account, signal_class, score, confidence) values ('tenant-A','x','buying',0.5,0.5)`); err == nil {
		t.Fatal("RLS WITH CHECK must reject a cross-tenant INSERT")
	}
}
