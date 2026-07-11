//go:build integration

// Live end-to-end idempotency proof for the CRM push service (G2, ADR-0030 acceptance #4): a redelivered
// push is a no-op against a fake CRM sink, asserted via the sink write count + the ledger. Needs both a
// real PostgreSQL (the ledger) and the egress client. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
package crm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/enrichment/waterfall/internal/crm"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

func TestCRMPush_Idempotent(t *testing.T) {
	dsn := os.Getenv("WATERFALL_PG_DSN")
	if dsn == "" {
		t.Skip("set WATERFALL_PG_DSN to run the CRM push idempotency test")
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
	apply := func(path string) {
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
	apply("../../migrations/0019_crm.sql")
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

	// Fake CRM sink: counts writes, and only accepts the egress-injected Bearer token.
	var writes int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&writes, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	egress := &http.Client{Transport: provider.NewAuthInjector(http.DefaultTransport, provider.StaticKeyResolver{"env-1": "tok-1"})}
	svc := crm.NewService(store, crm.NewPusher(egress))

	ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A", UserID: "u"})
	if err := store.SaveConnection(ctx, crm.Connection{ConnectionID: "c1", Provider: "hubspot", SecretRef: "env-1"}); err != nil {
		t.Fatalf("SaveConnection: %v", err)
	}
	body := json.RawMessage(`{"Ticker__c":"ACME"}`)

	// first push → executes (pushed=true); the sink writes once.
	if pushed, err := svc.Push(ctx, "c1", "acme.com", "d1", 1, sink.URL, body); err != nil || !pushed {
		t.Fatalf("first push = %v err=%v, want pushed", pushed, err)
	}
	// redelivery (same key) → deduplicated no-op (pushed=false); NO extra sink write.
	if pushed, err := svc.Push(ctx, "c1", "acme.com", "d1", 1, sink.URL, body); err != nil || pushed {
		t.Fatalf("redelivered push = %v err=%v, want no-op", pushed, err)
	}
	if got := atomic.LoadInt32(&writes); got != 1 {
		t.Fatalf("CRM sink received %d writes, want exactly 1 (idempotent)", got)
	}

	// a new field_map_version is a DISTINCT push (a remap) → executes again.
	if pushed, err := svc.Push(ctx, "c1", "acme.com", "d1", 2, sink.URL, body); err != nil || !pushed {
		t.Fatalf("remap push = %v err=%v, want pushed", pushed, err)
	}
	if got := atomic.LoadInt32(&writes); got != 2 {
		t.Fatalf("after remap: CRM sink writes = %d, want 2", got)
	}
}
