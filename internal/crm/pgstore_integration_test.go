//go:build integration

// Live tenant-isolation + idempotency + DSAR proof for the CRM store (ADR-0030, gates G1/G2) against a
// real PostgreSQL. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// Assertions run as a NON-superuser role (app_rls) so RLS is actually enforced (superusers bypass it).
package crm_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/crm"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

func principal(tenantID string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "u"})
}

func TestCRMRLS_Isolation(t *testing.T) {
	dsn := os.Getenv("WATERFALL_PG_DSN")
	if dsn == "" {
		t.Skip("set WATERFALL_PG_DSN to run the CRM RLS integration test")
	}
	admin, err := pg.Connect(pg.ParseDSN(dsn))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	tryExec := func(sql string) { _ = admin.Exec(sql) }
	tryExec("drop owned by app_rls cascade")
	tryExec("drop role if exists app_rls")
	tryExec("drop table if exists crm_connections, crm_field_maps, crm_push_ledger cascade")
	tryExec("drop function if exists app_current_tenant() cascade")

	applyFile := func(path string) {
		ddl, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := admin.Exec(string(ddl)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	// app_current_tenant() is the only thing 0019 needs from 0001; define it inline so this test is
	// self-contained (two integration tests share one DB — re-applying all of 0001 would collide on its
	// base tables).
	if err := admin.Exec("CREATE OR REPLACE FUNCTION app_current_tenant() RETURNS text LANGUAGE sql STABLE AS $f$ SELECT current_setting('app.current_tenant', true) $f$"); err != nil {
		t.Fatalf("define app_current_tenant: %v", err)
	}
	applyFile("../../migrations/0019_crm.sql")

	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := admin.Exec("grant select, insert, update, delete on crm_connections, crm_field_maps, crm_push_ledger to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	appCfg := pg.ParseDSN(dsn)
	appCfg.User = "app_rls"
	store := crm.NewStore(pg.NewPool(appCfg, 4))
	defer store.Close()

	ctxA := principal("tenant-A")
	ctxB := principal("tenant-B")

	// tenant-A configures a connection + a v1 field map.
	if err := store.SaveConnection(ctxA, crm.Connection{
		ConnectionID: "c1", Provider: "salesforce", SecretRef: "env-123", Config: json.RawMessage(`{"instance":"na1"}`),
	}); err != nil {
		t.Fatalf("SaveConnection A: %v", err)
	}
	if err := store.SaveFieldMap(ctxA, crm.FieldMap{
		ConnectionID: "c1", Version: 1, Mapping: json.RawMessage(`{"company_ticker":"Ticker__c"}`),
	}); err != nil {
		t.Fatalf("SaveFieldMap A: %v", err)
	}

	// read-back within the tenant.
	conn, ok, err := store.GetConnection(ctxA, "c1")
	if err != nil || !ok || conn.Provider != "salesforce" || conn.SecretRef != "env-123" {
		t.Fatalf("GetConnection A = %+v ok=%v err=%v", conn, ok, err)
	}
	if list, err := store.ListConnections(ctxA); err != nil || len(list) != 1 {
		t.Fatalf("ListConnections A = %d err=%v", len(list), err)
	}
	if fm, ok, err := store.LatestFieldMap(ctxA, "c1"); err != nil || !ok || fm.Version != 1 {
		t.Fatalf("LatestFieldMap A = %+v ok=%v err=%v", fm, ok, err)
	}

	// G2 idempotency: the same key twice — first records (true), second is a no-op (false).
	key := crm.PushKey("tenant-A", "c1", "acme.com", 1, "d1")
	rec := crm.PushRecord{ConnectionID: "c1", IdemKey: key, Record: "acme.com", FieldMapVersion: 1, DossierVersion: "d1"}
	if first, err := store.RecordPush(ctxA, rec); err != nil || !first {
		t.Fatalf("first RecordPush must insert (first=%v err=%v)", first, err)
	}
	if second, err := store.RecordPush(ctxA, rec); err != nil || second {
		t.Fatalf("redelivered RecordPush must be a no-op (second=%v err=%v)", second, err)
	}

	// tenant-B sees NONE of A's connections/pushes (RLS G1) and cannot read A's connection.
	if list, err := store.ListConnections(ctxB); err != nil || len(list) != 0 {
		t.Fatalf("tenant-B must NOT see A's connections (n=%d err=%v)", len(list), err)
	}
	if _, ok, err := store.GetConnection(ctxB, "c1"); err != nil || ok {
		t.Fatalf("tenant-B must NOT read A's connection (ok=%v err=%v)", ok, err)
	}
	// The UNIQUE(tenant_id, idem_key) constraint is per-tenant: tenant-B recording the same key inserts its
	// own row (it cannot collide with, or observe, A's ledger).
	if bFirst, err := store.RecordPush(ctxB, rec); err != nil || !bFirst {
		t.Fatalf("tenant-B first push must insert its own row (first=%v err=%v)", bFirst, err)
	}

	// DSAR: an erasure records the downstream obligation for A's pushes only; idempotent on re-run.
	n, err := store.MarkErasurePending(ctxA, "acme.com")
	if err != nil || n != 1 {
		t.Fatalf("MarkErasurePending A = %d err=%v, want 1", n, err)
	}
	if n2, err := store.MarkErasurePending(ctxA, "acme.com"); err != nil || n2 != 0 {
		t.Fatalf("second MarkErasurePending = %d err=%v, want 0 (idempotent)", n2, err)
	}

	// Fail-closed: no principal ⇒ error, never cross-tenant.
	if _, err := store.ListConnections(context.Background()); err == nil {
		t.Fatal("store must reject an unauthenticated context")
	}
}
