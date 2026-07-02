//go:build integration

// Live tenant-isolation (RLS) proof for gate G1 — the docs/21 §1 release-blocker test,
// run against a real PostgreSQL. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// All isolation assertions run as a NON-superuser role (app_rls): superusers and BYPASSRLS
// roles are exempt from row-level security, so testing as one would prove nothing.
package pgstore_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgstore"
	"github.com/enrichment/waterfall/internal/tenant"
)

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run Postgres RLS integration test")
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

func scalar(t *testing.T, c *pg.Conn, sql string) string {
	t.Helper()
	res, err := c.Query(sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return ""
	}
	return *res.Rows[0][0]
}

func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	// Idempotent teardown so the test re-runs cleanly.
	tryExec(admin, "drop owned by app_rls cascade")
	tryExec(admin, "drop role if exists app_rls")
	tryExec(admin, "drop table if exists field_versions, idempotency_ledger, cost_ledger cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")

	ddl, err := os.ReadFile("../../migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	mustExec(t, admin, "create role app_rls login nosuperuser")
	// UPDATE is needed for the cost-ledger reservation (G4); SELECT/INSERT for the rest.
	mustExec(t, admin, "grant select, insert, update on field_versions, idempotency_ledger, cost_ledger to app_rls")
}

func TestRLS_TenantIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls" // the role RLS actually applies to

	raw, err := pg.Connect(appCfg)
	if err != nil {
		t.Fatalf("connect app_rls: %v", err)
	}
	defer raw.Close()

	insert := func(tenantGUC, subject string) {
		mustExec(t, raw, "set app.current_tenant = "+quote(tenantGUC))
		mustExec(t, raw, `insert into field_versions
			(tenant_id, subject_id, field, value, confidence, provider, cost_credits, obs_confidence, idempotency_key, observed_at)
			values (current_setting('app.current_tenant'), $1, 'work_email', 'v', 0.9, 'acme', 0, 0.9, 'k', $2)`,
			subject, time.Now())
	}
	insert("tenant-A", "subj-A")
	insert("tenant-B", "subj-B")

	// (1) Cross-tenant read is blocked by RLS: as tenant-A, only A's row is visible.
	mustExec(t, raw, "set app.current_tenant = 'tenant-A'")
	if got := scalar(t, raw, "select count(*) from field_versions"); got != "1" {
		t.Fatalf("tenant-A should see exactly its 1 row, saw %s", got)
	}
	if got := scalar(t, raw, "select count(*) from field_versions where subject_id = 'subj-B'"); got != "0" {
		t.Fatalf("tenant-A must NOT see tenant-B's row, saw %s", got)
	}
	mustExec(t, raw, "set app.current_tenant = 'tenant-B'")
	if got := scalar(t, raw, "select count(*) from field_versions"); got != "1" {
		t.Fatalf("tenant-B should see exactly its 1 row, saw %s", got)
	}

	// (2) WITH CHECK blocks writing into another tenant: as tenant-A, inserting a row
	// stamped tenant-B must be rejected by the policy.
	mustExec(t, raw, "set app.current_tenant = 'tenant-A'")
	err = raw.ExecParams(`insert into field_versions
		(tenant_id, subject_id, field, value, confidence, provider, cost_credits, obs_confidence, idempotency_key, observed_at)
		values ('tenant-B', 'evil', 'work_email', 'v', 0.9, 'acme', 0, 0.9, 'k', $1)`, time.Now())
	if err == nil {
		t.Fatal("RLS WITH CHECK must reject a cross-tenant INSERT, but it succeeded")
	}

	// (3) The application store enforces the same isolation via the principal.
	store, err := pgstore.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctxA := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A"})
	ctxB := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-B"})
	fv := domain.FieldValue{
		Field:      domain.FieldWorkEmail,
		Value:      "jane@acme.com",
		Confidence: 0.9,
		Prov: domain.Provenance{
			Provider: "acme", ObservedAt: time.Now(), CostCredits: 5,
			Confidence: 0.9, IdempotencyKey: "idem-store",
		},
	}
	if err := store.Append(ctxA, "subj-store", fv); err != nil {
		t.Fatalf("append under tenant-A: %v", err)
	}
	// tenant-A sees its write...
	cur, err := store.Current(ctxA, "subj-store")
	if err != nil {
		t.Fatalf("current A: %v", err)
	}
	if got, ok := cur[domain.FieldWorkEmail]; !ok || got.Value != "jane@acme.com" {
		t.Fatalf("tenant-A should read its own value, got %+v", cur)
	}
	// ...tenant-B cannot see tenant-A's write.
	curB, err := store.Current(ctxB, "subj-store")
	if err != nil {
		t.Fatalf("current B: %v", err)
	}
	if len(curB) != 0 {
		t.Fatalf("tenant-B must NOT see tenant-A's data, got %+v", curB)
	}

	// (4) Fail-closed: no principal in context => ErrNoPrincipal, never "all tenants".
	if err := store.Append(context.Background(), "x", fv); err == nil {
		t.Fatal("store must reject an unauthenticated context")
	}
}

// quote single-quote-escapes a GUC value for a SET statement (test-only; values are literals).
func quote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}
