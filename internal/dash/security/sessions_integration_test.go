//go:build integration

// Live-Postgres proof for the OI-RB-2 bulk session-revoke primitive
// (security.Sessions.RevokeAllForUser). It runs as the non-superuser dash_app role (superusers
// bypass RLS, proving nothing) so the per-user scoping AND the G1 tenant scoping of the bulk UPDATE
// are both exercised against real row-level security. Invoke via scripts/run-rls-test.sh or with
// WATERFALL_PG_DSN set.
package security_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_app"

// dashTables is every table created by migration 0004 (mirrors the e2e harness).
var dashTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the security integration tests")
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

// setupDashSchema rebuilds migration 0004 cleanly and provisions the non-superuser dash_app role.
// Self-contained and idempotent, matching the internal/dash/e2e harness so this package can run in
// any position of the shared-database integration sequence.
func setupDashSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+appRole+" cascade")
	tryExec(admin, "drop role if exists "+appRole)
	tryExec(admin, "drop table if exists mfa_used_steps, dash_admin_idempotency cascade")
	tryExec(admin, "drop table if exists "+strings.Join(dashTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")

	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	ddl, err := os.ReadFile("../../../migrations/0004_dash_identity_rbac.sql")
	if err != nil {
		t.Fatalf("read migration 0004: %v", err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply migration 0004: %v", err)
	}

	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+strings.Join(dashTables, ", ")+" to "+appRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+appRole)
}

func principal(tenantID, role string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: tenantID, Scopes: []string{"role:" + role},
	})
}

// activeCount returns how many live sessions userID has in ctx's tenant scope (List returns only
// un-revoked rows).
func activeCount(t *testing.T, s *security.Sessions, ctx context.Context, userID string) int {
	t.Helper()
	items, err := s.List(ctx, userID, false)
	if err != nil {
		t.Fatalf("List(%s): %v", userID, err)
	}
	return len(items)
}

// TestSessionsRevokeAllForUser proves the bulk revoke: it cuts every live session for one user in a
// single statement, leaves other users untouched (per-user scoping), is idempotent (a second call
// returns 0), and cannot reach across tenants (G1 — a foreign-tenant binding revokes nothing).
func TestSessionsRevokeAllForUser(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupDashSchema(t, admin)

	// Seed two tenants and three users (userA/userB in tenant-a, userC in tenant-b) as superuser.
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values
		('tenant-a','A','customer','active'), ('tenant-b','B','customer','active')`)
	const (
		userA = "a1111111-1111-4111-8111-111111111111"
		userB = "b2222222-2222-4222-8222-222222222222"
		userC = "c3333333-3333-4333-8333-333333333333"
	)
	mustExec(t, admin, `insert into users (id, tenant_id, email, password_hash, role) values
		($1,'tenant-a','a@x','x','tenant_admin'),
		($2,'tenant-a','b@x','x','tenant_user'),
		($3,'tenant-b','c@x','x','tenant_user')`, userA, userB, userC)

	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
	defer pool.Close()
	store := db.New(pool)
	sessions := security.NewSessions(store)

	ctxA := principal("tenant-a", "tenant_admin")
	ctxB := principal("tenant-b", "tenant_admin")

	// Open 3 sessions for userA, 2 for userB (tenant-a), 2 for userC (tenant-b).
	mk := func(ctx context.Context, uid string, n int) {
		for i := 0; i < n; i++ {
			if _, _, err := sessions.Create(ctx, uid, "10.0.0.1", "test-agent", false); err != nil {
				t.Fatalf("Create session for %s: %v", uid, err)
			}
		}
	}
	mk(ctxA, userA, 3)
	mk(ctxA, userB, 2)
	mk(ctxB, userC, 2)

	if got := activeCount(t, sessions, ctxA, userA); got != 3 {
		t.Fatalf("precondition: userA active = %d, want 3", got)
	}

	// Bulk revoke userA -> 3 cut.
	n, err := sessions.RevokeAllForUser(ctxA, userA)
	if err != nil {
		t.Fatalf("RevokeAllForUser(userA): %v", err)
	}
	if n != 3 {
		t.Fatalf("RevokeAllForUser(userA) = %d, want 3", n)
	}
	if got := activeCount(t, sessions, ctxA, userA); got != 0 {
		t.Fatalf("after revoke: userA active = %d, want 0", got)
	}

	// userB untouched (per-user scoping, same tenant).
	if got := activeCount(t, sessions, ctxA, userB); got != 2 {
		t.Fatalf("userB active = %d, want 2 (must be untouched)", got)
	}

	// Idempotent: nothing left to revoke.
	if n, err := sessions.RevokeAllForUser(ctxA, userA); err != nil || n != 0 {
		t.Fatalf("second RevokeAllForUser(userA) = (%d,%v), want (0,nil)", n, err)
	}

	// G1: a tenant-a binding cannot revoke userC's tenant-b sessions (RLS scopes the UPDATE).
	if n, err := sessions.RevokeAllForUser(ctxA, userC); err != nil || n != 0 {
		t.Fatalf("cross-tenant RevokeAllForUser(userC from tenant-a) = (%d,%v), want (0,nil)", n, err)
	}
	if got := activeCount(t, sessions, ctxB, userC); got != 2 {
		t.Fatalf("userC (tenant-b) active = %d, want 2 (cross-tenant revoke must be a no-op)", got)
	}

	t.Log("PASS: RevokeAllForUser is bulk, per-user scoped, idempotent, and G1 tenant-isolated")
}
