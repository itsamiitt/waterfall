//go:build integration

// Live tenant-isolation proof for the Intent dashboard read model (gate G1) against a real
// PostgreSQL, over the dashboard dual-GUC RLS seam. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// Assertions run as a NON-superuser role (app_rls) whose Principal carries a role scope, so the
// db.Store binds both app.current_tenant and app.current_role.
package intent_test

import (
	"context"
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/intent"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the intent dashboard RLS integration test")
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

func TestIntentDashboard_TenantIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	tryExec(admin, "drop owned by app_rls cascade")
	tryExec(admin, "drop role if exists app_rls")
	tryExec(admin, "drop table if exists intent_scores, field_versions, idempotency_ledger, cost_ledger cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")
	applyFile(t, admin, "../../../migrations/0001_init.sql")
	applyFile(t, admin, "../../../migrations/0016_intent.sql")
	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := admin.Exec("grant select, insert, update, delete on intent_scores to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// Seed scores for two Tenants (as superuser; RLS bypassed on the seed insert).
	if err := admin.Exec(`insert into intent_scores (tenant_id, account, signal_class, score, confidence, signal_count, config_version)
		values ('tenant-A','acme.com','hiring',0.8,0.7,2,'v1'),
		       ('tenant-A','acme.com','buying',0.6,0.5,1,'v1'),
		       ('tenant-B','other.com','buying',0.5,0.5,1,'v1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	appCfg := cfg
	appCfg.User = "app_rls"
	store := db.New(pg.NewPool(appCfg, 4))
	svc := intent.NewService(store)

	ctxA := principal("tenant-A", "tenant_admin")
	ctxB := principal("tenant-B", "tenant_admin")

	// tenant-A sees its two classes (hiring first).
	got, err := svc.Account(ctxA, "acme.com")
	if err != nil {
		t.Fatalf("Account A: %v", err)
	}
	if len(got) != 2 || got[0].Class != "hiring" || got[0].Score != 0.8 {
		t.Fatalf("A account = %+v", got)
	}
	// tenant-B sees none of tenant-A's account (RLS).
	if gotB, err := svc.Account(ctxB, "acme.com"); err != nil || len(gotB) != 0 {
		t.Fatalf("tenant-B must NOT see tenant-A's account (n=%d err=%v)", len(gotB), err)
	}

	// List is tenant-scoped: A sees acme.com (top hiring), B sees only other.com.
	listA, err := svc.List(ctxA, 50)
	if err != nil || len(listA) != 1 || listA[0].Account != "acme.com" || listA[0].TopClass != "hiring" {
		t.Fatalf("List A = %+v err=%v", listA, err)
	}
	listB, err := svc.List(ctxB, 50)
	if err != nil || len(listB) != 1 || listB[0].Account != "other.com" {
		t.Fatalf("List B = %+v err=%v", listB, err)
	}

	// Fail-closed: no principal ⇒ error, never cross-tenant.
	if _, err := svc.Account(context.Background(), "acme.com"); err == nil {
		t.Fatal("service must reject an unauthenticated context")
	}
}
