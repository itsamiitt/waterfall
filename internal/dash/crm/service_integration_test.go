//go:build integration

// Live tenant-isolation proof for the CRM dashboard read model (gate G1) against a real PostgreSQL, over
// the dashboard dual-GUC RLS seam. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// Assertions run as a NON-superuser role (app_rls) whose Principal carries a role scope.
package crm_test

import (
	"context"
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/crm"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

func principal(tenantID, role string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "u", Scopes: []string{"role:" + role}})
}

func TestCRMDashboard_TenantIsolation(t *testing.T) {
	dsn := os.Getenv("WATERFALL_PG_DSN")
	if dsn == "" {
		t.Skip("set WATERFALL_PG_DSN to run the crm dashboard RLS integration test")
	}
	admin, err := pg.Connect(pg.ParseDSN(dsn))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	try := func(sql string) { _ = admin.Exec(sql) }
	try("drop owned by app_rls cascade")
	try("drop role if exists app_rls")
	try("drop table if exists crm_connections, crm_field_maps, crm_push_ledger cascade")
	try("drop function if exists app_current_tenant() cascade")
	try("drop function if exists app_current_role() cascade")

	exec := func(sql string) {
		if err := admin.Exec(sql); err != nil {
			t.Fatalf("exec failed: %v\n%s", err, sql)
		}
	}
	// Dual-GUC primitives (0001 app_current_tenant + 0004 app_current_role) inline; then crm tables (0019).
	exec("CREATE OR REPLACE FUNCTION app_current_tenant() RETURNS text LANGUAGE sql STABLE AS $f$ SELECT current_setting('app.current_tenant', true) $f$")
	exec("CREATE OR REPLACE FUNCTION app_current_role() RETURNS text LANGUAGE sql STABLE AS $f$ SELECT current_setting('app.current_role', true) $f$")
	ddl, err := os.ReadFile("../../../migrations/0019_crm.sql")
	if err != nil {
		t.Fatalf("read 0019: %v", err)
	}
	exec(string(ddl))

	exec("create role app_rls login nosuperuser")
	exec("grant select, insert, update, delete on crm_connections, crm_field_maps, crm_push_ledger to app_rls")
	exec(`insert into crm_connections (tenant_id, connection_id, provider, status, secret_ref)
		values ('tenant-A','a1','salesforce','active','env-a'),
		       ('tenant-B','b1','hubspot','active','env-b')`)

	appCfg := pg.ParseDSN(dsn)
	appCfg.User = "app_rls"
	store := db.New(pg.NewPool(appCfg, 4))
	svc := crm.NewService(store)

	ctxA := principal("tenant-A", "tenant_admin")
	ctxB := principal("tenant-B", "tenant_admin")

	// tenant-A lists its connection; tenant-B lists only its own.
	if la, err := svc.List(ctxA, 50); err != nil || len(la) != 1 || la[0].ConnectionID != "a1" || la[0].Provider != "salesforce" {
		t.Fatalf("List A = %+v err=%v", la, err)
	}
	if lb, err := svc.List(ctxB, 50); err != nil || len(lb) != 1 || lb[0].ConnectionID != "b1" {
		t.Fatalf("List B = %+v err=%v", lb, err)
	}

	// tenant-A reads a1; tenant-B cannot (RLS).
	if c, ok, err := svc.Get(ctxA, "a1"); err != nil || !ok || c.Provider != "salesforce" {
		t.Fatalf("Get A a1 = %+v ok=%v err=%v", c, ok, err)
	}
	if _, ok, err := svc.Get(ctxB, "a1"); err != nil || ok {
		t.Fatalf("tenant-B must NOT read tenant-A's connection (ok=%v err=%v)", ok, err)
	}

	// Fail-closed: no principal ⇒ error.
	if _, err := svc.List(context.Background(), 50); err == nil {
		t.Fatal("service must reject an unauthenticated context")
	}
}
