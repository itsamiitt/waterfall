//go:build integration

// Package e2e_test is the Phase P0 live-Postgres proof for the dashboard identity/RBAC surface:
//
//   - TestDashRLSZeroRows — the gate-G1 release blocker (doc 13 §3.1): for every table in
//     migration 0004, a row written as Tenant A is invisible to Tenant B, cross-tenant INSERT is
//     rejected by WITH CHECK, and sessions/mfa_recovery_codes/secret_envelopes stay invisible even
//     to role operator.
//   - TestDashLoginMFAAndSecurity — the login/MFA E2E over httptest (doc 12 P0 #3): login (PBKDF2)
//     -> mfa/verify (RFC 6238) -> /auth/me, each step chained into audit_log which verifies clean;
//     plus the CSRF-negative (#4) and Idempotency-Key-409 (#5) acceptance checks.
//
// Runs as non-superuser dash_app (superusers bypass RLS, proving nothing). Invoke via
// scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/approvals"
	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/httpx"
	"github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/dash/providers"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_app"

// zeroHash is 32 zero bytes in Postgres bytea hex text form (genesis prev_hash shape).
var zeroHash = `\x` + strings.Repeat("00", 32)

// dashTables is every table created by migration 0004 (the 9 the RLS blocker must cover).
var dashTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the dashboard integration tests")
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

// setupDashSchema rebuilds migration 0004 cleanly and provisions the non-superuser dash_app role.
// It is idempotent and self-contained (independent of migration-tracking state left by sibling
// integration packages in the shared database).
func setupDashSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+appRole+" cascade")
	tryExec(admin, "drop role if exists "+appRole)
	tryExec(admin, "drop table if exists mfa_used_steps, dash_admin_idempotency cascade")
	tryExec(admin, "drop table if exists "+strings.Join(dashTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")

	// app_current_tenant() lives in migration 0001; ensure it exists for 0004's policies.
	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	ddl, err := os.ReadFile("../../../migrations/0004_dash_identity_rbac.sql")
	if err != nil {
		t.Fatalf("read migration 0004: %v", err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply migration 0004: %v", err)
	}

	// Migration 0011 tables the hardened login/idempotency paths need (mfa_used_steps for the TOTP
	// single-use guard, dash_admin_idempotency for the durable admin idempotency ledger). Both depend
	// only on 0004; created inline so the 0004-only P0 schema exercises the hardened paths.
	mustExec(t, admin, `create table mfa_used_steps (
		tenant_id text not null, user_id uuid not null references users(id),
		time_step bigint not null, used_at timestamptz not null default now(),
		primary key (user_id, time_step))`)
	mustExec(t, admin, `create table dash_admin_idempotency (
		tenant_id text not null, idempotency_key text not null, body_hash bytea not null,
		status int, response jsonb, created_at timestamptz not null default now(),
		primary key (tenant_id, idempotency_key))`)
	for _, tbl := range []string{"mfa_used_steps", "dash_admin_idempotency"} {
		mustExec(t, admin, "alter table "+tbl+" enable row level security")
		mustExec(t, admin, "alter table "+tbl+" force row level security")
		mustExec(t, admin, "create policy "+tbl+"_iso on "+tbl+
			" using (tenant_id = app_current_tenant()) with check (tenant_id = app_current_tenant())")
	}

	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+strings.Join(dashTables, ", ")+" to "+appRole)
	mustExec(t, admin, "grant select, insert, update, delete on mfa_used_steps, dash_admin_idempotency to "+appRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+appRole)
}

// TestDashRLSZeroRows is the gate-G1 release blocker for the 0004 tables (doc 13 §3.1).
func TestDashRLSZeroRows(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupDashSchema(t, admin)

	// Fixtures the RLS inserts reference, seeded as superuser (bypasses RLS).
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('tenant-a','A','customer','active'),('tenant-b','B','customer','active')`)
	const ua = "11111111-1111-4111-8111-111111111111"
	const ub = "22222222-2222-4222-8222-222222222222"
	mustExec(t, admin, `insert into users (id, tenant_id, email, password_hash, role) values
		($1,'tenant-a','a@x','x','tenant_user'), ($2,'tenant-b','b@x','x','tenant_user')`, ua, ub)

	appCfg := cfg
	appCfg.User = appRole
	raw, err := pg.Connect(appCfg)
	if err != nil {
		t.Fatalf("connect %s: %v", appRole, err)
	}
	defer raw.Close()

	// Bind both GUCs via set_config (matching the dual-GUC tx helper). SET app.current_role = ...
	// is a syntax error because current_role is a reserved word; set_config takes the name as a
	// string literal and sidesteps that.
	set := func(tenantID, role string) {
		mustExec(t, raw, `select set_config('app.current_tenant', $1, false)`, tenantID)
		mustExec(t, raw, `select set_config('app.current_role', $1, false)`, role)
	}

	// --- (1) insert one row per Class-T table as tenant-a ---
	set("tenant-a", "tenant_admin")
	mustExec(t, raw, `insert into users (id, tenant_id, email, password_hash, role) values ($1,'tenant-a','rls-a@x','x','tenant_user')`, newUUID())
	mustExec(t, raw, `insert into mfa_recovery_codes (tenant_id, user_id, code_hash) values ('tenant-a',$1,'\x01')`, ua)
	mustExec(t, raw, `insert into sessions (id, tenant_id, user_id, csrf_token, idle_expires_at, absolute_expires_at) values ('sess-rls-a','tenant-a',$1,'c', now()+interval '30 min', now()+interval '12 hour')`, ua)
	mustExec(t, raw, `insert into ip_allowlists (id, tenant_id, cidr) values ($1,'tenant-a','10.0.0.0/8')`, newUUID())
	mustExec(t, raw, `insert into audit_log (tenant_id, seq, action, prev_hash, hash) values ('tenant-a',1,'rls',`+q(zeroHash)+`,`+q(zeroHash)+`)`)
	mustExec(t, raw, `insert into audit_chain_heads (tenant_id, last_seq, last_hash) values ('tenant-a',1,`+q(zeroHash)+`)`)
	mustExec(t, raw, `insert into api_access_log (tenant_id, method, route, status, dur_ms) values ('tenant-a','GET','/x',200,1)`)
	// secret_envelopes is Class P: only the platform binding may write it.
	set("platform", "operator")
	mustExec(t, raw, `insert into secret_envelopes (id, kind, master_key_id, dek_wrapped, nonce, ciphertext) values ($1,'totp_seed','k','\x01','\x01','\x01')`, newUUID())

	// --- (2) Tenant B sees zero of Tenant A's rows ---
	set("tenant-b", "tenant_admin")
	assertZero(t, raw, "tenants", "select count(*) from tenants where id = 'tenant-a'")
	assertZero(t, raw, "users", "select count(*) from users where tenant_id = 'tenant-a'")
	assertZero(t, raw, "mfa_recovery_codes", "select count(*) from mfa_recovery_codes where tenant_id = 'tenant-a'")
	assertZero(t, raw, "sessions", "select count(*) from sessions where tenant_id = 'tenant-a'")
	assertZero(t, raw, "ip_allowlists", "select count(*) from ip_allowlists where tenant_id = 'tenant-a'")
	assertZero(t, raw, "audit_log", "select count(*) from audit_log where tenant_id = 'tenant-a'")
	assertZero(t, raw, "audit_chain_heads", "select count(*) from audit_chain_heads where tenant_id = 'tenant-a'")
	assertZero(t, raw, "api_access_log", "select count(*) from api_access_log where tenant_id = 'tenant-a'")
	assertZero(t, raw, "secret_envelopes", "select count(*) from secret_envelopes")

	// --- (3) cross-tenant INSERT blocked by WITH CHECK (as tenant-a, stamped tenant-b) ---
	set("tenant-a", "tenant_admin")
	assertBlocked(t, raw, "users", `insert into users (id, tenant_id, email, password_hash, role) values ('`+newUUID()+`','tenant-b','evil@x','x','tenant_user')`)
	assertBlocked(t, raw, "sessions", `insert into sessions (id, tenant_id, user_id, csrf_token, idle_expires_at, absolute_expires_at) values ('evil','tenant-b','`+ub+`','c', now(), now())`)
	assertBlocked(t, raw, "audit_log", `insert into audit_log (tenant_id, seq, action, prev_hash, hash) values ('tenant-b',9,'evil',`+zeroHash+`::bytea,`+zeroHash+`::bytea)`)
	assertBlocked(t, raw, "api_access_log", `insert into api_access_log (tenant_id, method, route, status, dur_ms) values ('tenant-b','GET','/x',200,1)`)

	// --- (4) operator with a customer-tenant binding still sees ZERO of the never-operator-readable
	// tables (sessions, mfa_recovery_codes, secret_envelopes) — no blanket operator policy ---
	set("tenant-b", "operator")
	assertZero(t, raw, "sessions (operator)", "select count(*) from sessions where tenant_id = 'tenant-a'")
	assertZero(t, raw, "mfa_recovery_codes (operator)", "select count(*) from mfa_recovery_codes where tenant_id = 'tenant-a'")
	assertZero(t, raw, "secret_envelopes (operator)", "select count(*) from secret_envelopes")

	t.Log("PASS: RLS zero-rows across all 9 tables of migration 0004 (G1 release blocker)")
}

func assertZero(t *testing.T, c *pg.Conn, name, sql string) {
	t.Helper()
	if got := scalar(t, c, sql); got != "0" {
		t.Fatalf("%s: cross-tenant SELECT returned %s rows, want 0", name, got)
	}
}

func assertBlocked(t *testing.T, c *pg.Conn, name, sql string) {
	t.Helper()
	if err := c.Exec(sql); err == nil {
		t.Fatalf("%s: cross-tenant INSERT succeeded but WITH CHECK must reject it", name)
	}
}

// TestDashLoginMFAAndSecurity drives the login/MFA/audit E2E plus the CSRF and idempotency
// acceptance checks over a real httptest server backed by live RLS-enforced Postgres.
func TestDashLoginMFAAndSecurity(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupDashSchema(t, admin)
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('acme','Acme','customer','active')`)

	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
	defer pool.Close()
	store := db.New(pool)

	// Envelope-encryption backend with a fresh 32-byte master key.
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	backend := secrets.NewPGBackend(store, kr, []byte("test-pepper"))

	users := security.NewUsers(store, backend, "Waterfall")
	sessions := security.NewSessions(store)
	ipallow := security.NewIPAllow(store)
	access := security.NewAccessLog(store, 1024)
	access.Start(100 * time.Millisecond)
	defer access.Stop()
	auditLog := audit.New(store)

	// --- seed a tenant_admin user with MFA enrolled (programmatically, so we know the seed) ---
	adminCtx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "acme", UserID: "seed", Scopes: []string{"role:tenant_admin"},
	})
	pwHash, err := security.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid, err := users.Create(adminCtx, "ops@acme.example", pwHash, "tenant_admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	seed, _, err := users.EnrollMFA(adminCtx, uid, "ops@acme.example")
	if err != nil {
		t.Fatalf("enroll mfa: %v", err)
	}
	if _, err := users.ConfirmMFA(adminCtx, uid, security.GenerateTOTP(seed, time.Now()), time.Now()); err != nil {
		t.Fatalf("confirm mfa: %v", err)
	}

	srv := httpx.NewServer(httpx.Deps{
		Store: store, Auth: httpx.NewSessionOrJWT(sessions, nil),
		Users: users, Sessions: sessions, IPAllow: ipallow, Access: access,
		Secrets: backend, Audit: auditLog, Issuer: "Waterfall",
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cl := &client{t: t, base: ts.URL}

	// --- login: MFA-enrolled tenant_admin => mfa_required ---
	st, body := cl.do("POST", "/v1/admin/auth/login", "", `{"email":"ops@acme.example","password":"correct horse battery staple"}`)
	if st != 200 || body["status"] != "mfa_required" {
		t.Fatalf("login = %d %v, want 200 mfa_required", st, body)
	}
	if cl.cookie == "" {
		t.Fatal("login did not set dash_session cookie")
	}

	// pre-MFA: a protected route is gated
	if st, _ := cl.do("GET", "/v1/admin/auth/me", "", ""); st != 401 {
		t.Fatalf("pre-MFA /auth/me = %d, want 401", st)
	}

	// --- mfa/verify: RFC 6238 code from the same seed ---
	st, body = cl.do("POST", "/v1/admin/auth/mfa/verify", "", `{"code":"`+security.GenerateTOTP(seed, time.Now())+`"}`)
	if st != 200 || body["status"] != "ok" {
		t.Fatalf("mfa/verify = %d %v, want 200 ok", st, body)
	}
	cl.csrf, _ = body["csrf_token"].(string)
	if cl.csrf == "" {
		t.Fatal("mfa/verify did not return csrf_token")
	}

	// --- /auth/me returns the principal ---
	st, body = cl.do("GET", "/v1/admin/auth/me", "", "")
	if st != 200 {
		t.Fatalf("/auth/me = %d, want 200", st)
	}
	if body["tenant_id"] != "acme" || body["role"] != "tenant_admin" {
		t.Fatalf("/auth/me principal mismatch: %v", body)
	}
	if usr, _ := body["user"].(map[string]any); usr["id"] != uid {
		t.Fatalf("/auth/me user id = %v, want %s", usr["id"], uid)
	}

	// --- CSRF negative (#4): mutating request without X-CSRF-Token => 403 csrf_required ---
	st, body = cl.doRaw("POST", "/v1/admin/users", cl.cookie, "" /*no csrf*/, "idem-csrf", `{"email":"x@acme.example","role":"tenant_user"}`)
	if st != 403 || errCode(body) != "csrf_required" {
		t.Fatalf("CSRF-negative = %d %v, want 403 csrf_required", st, body)
	}

	// --- Idempotency-Key 409 (#5): same key, different body => 409 idempotency_key_reuse ---
	st, body = cl.doWrite("POST", "/v1/admin/users", "idem-1", `{"email":"first@acme.example","role":"tenant_user"}`)
	if st != 201 {
		t.Fatalf("first user create = %d %v, want 201", st, body)
	}
	st, body = cl.doWrite("POST", "/v1/admin/users", "idem-1", `{"email":"second@acme.example","role":"tenant_admin"}`)
	if st != 409 || errCode(body) != "idempotency_key_reuse" {
		t.Fatalf("idempotency reuse = %d %v, want 409 idempotency_key_reuse", st, body)
	}

	// --- audit chain: every step appended a row and the chain verifies clean ---
	st, list := cl.do("GET", "/v1/admin/audit-log", "", "")
	if st != 200 {
		t.Fatalf("/audit-log = %d", st)
	}
	items, _ := list["items"].([]any)
	if len(items) < 3 {
		t.Fatalf("expected >=3 audit rows (login, mfa login, user_create), got %d", len(items))
	}
	st, verify := cl.do("GET", "/v1/admin/audit-log/verify", "", "")
	if st != 200 || verify["ok"] != true {
		t.Fatalf("/audit-log/verify = %d %v, want 200 ok=true", st, verify)
	}

	// --- access log async writer flushed the request telemetry ---
	access.Flush(context.Background())
	st, alog := cl.do("GET", "/v1/admin/access-log", "", "")
	if st != 200 {
		t.Fatalf("/access-log = %d", st)
	}
	if arr, _ := alog["items"].([]any); len(arr) == 0 {
		t.Fatal("access-log returned no rows; async writer never persisted telemetry")
	}

	t.Log("PASS: login -> mfa/verify -> /auth/me with audit chain verify; CSRF-negative 403; idempotency 409")
}

// --- HTTP client helper (manual cookie/CSRF/idempotency management) ---

type client struct {
	t      *testing.T
	base   string
	cookie string
	csrf   string
}

// do issues a request carrying the tracked cookie and (for writes) the tracked CSRF token and the
// given idempotency key, decoding the JSON response and refreshing the cookie from Set-Cookie.
func (c *client) do(method, path, idemKey, body string) (int, map[string]any) {
	return c.doRaw(method, path, c.cookie, c.csrf, idemKey, body)
}

// doWrite is a mutating request with the tracked CSRF token and an explicit idempotency key.
func (c *client) doWrite(method, path, idemKey, body string) (int, map[string]any) {
	return c.doRaw(method, path, c.cookie, c.csrf, idemKey, body)
}

func (c *client) doRaw(method, path, cookie, csrf, idemKey, body string) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	if cookie != "" {
		req.Header.Set("Cookie", "dash_session="+cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == "dash_session" && ck.Value != "" {
			c.cookie = ck.Value
		}
	}
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return resp.StatusCode, m
}

// errCode extracts error.code from a uniform error body.
func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// quote single-quote-escapes a GUC/literal value for a SET or inline SQL (test-only; literals).
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// q wraps a bytea hex literal as a quoted ::bytea value for inline INSERTs.
func q(hexLiteral string) string { return "'" + hexLiteral + "'::bytea" }

// newUUID mints an RFC 4122 v4 uuid for test fixtures.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hexd = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[j] = '-'
			j++
		}
		out[j] = hexd[b[i]>>4]
		out[j+1] = hexd[b[i]&0x0f]
		j += 2
	}
	return string(out)
}

// TestDashFeatureWiring proves the P1 route integration: providers/keys feature routes mounted
// behind httpx.Server.FeatureChain get single authentication plus the shared CSRF / MFA / IP
// enforcement, exactly as cmd/dashboardd composes them. The security-critical assertion is that a
// mutating feature request without X-CSRF-Token is rejected by the shared chain (403 csrf_required)
// before reaching the feature handler.
func TestDashFeatureWiring(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	// This test exercises the feature (providers/keys) routes, which need migration 0005 in addition
	// to 0004. Drop 0005 objects first so setupDashSchema's 0004 rebuild is clean, then re-apply 0005
	// and grant the app role on its tables (mirrors the providers package's setup).
	tryExec(admin, "drop view if exists providers_catalog cascade")
	tryExec(admin, "drop table if exists providers, key_pools, provider_keys, key_pool_members, key_budgets, key_import_batches, health_schedules, rotation_triggers cascade")
	setupDashSchema(t, admin)
	ddl5, err := os.ReadFile("../../../migrations/0005_dash_providers_keys.sql")
	if err != nil {
		t.Fatalf("read migration 0005: %v", err)
	}
	if err := admin.Exec(string(ddl5)); err != nil {
		t.Fatalf("apply migration 0005: %v", err)
	}
	mustExec(t, admin, "alter view providers_catalog set (security_invoker = true)")
	const feat0005 = "providers, key_pools, provider_keys, key_pool_members, key_budgets, key_import_batches, health_schedules, rotation_triggers"
	mustExec(t, admin, "grant select, insert, update, delete on "+feat0005+" to "+appRole)
	mustExec(t, admin, "grant select on providers_catalog to "+appRole)

	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
	defer pool.Close()
	store := db.New(pool)

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	backend := secrets.NewPGBackend(store, kr, []byte("test-pepper"))

	users := security.NewUsers(store, backend, "Waterfall")
	sessions := security.NewSessions(store)
	ipallow := security.NewIPAllow(store)
	access := security.NewAccessLog(store, 1024)
	access.Start(100 * time.Millisecond)
	defer access.Stop()
	auditLog := audit.New(store)

	// Seed an operator user in the platform Tenant (operators manage the Provider catalog).
	opCtx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "platform", UserID: "seed", Scopes: []string{"role:operator"},
	})
	pwHash, err := security.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid, err := users.Create(opCtx, "op@platform.example", pwHash, "operator")
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	seed, _, err := users.EnrollMFA(opCtx, uid, "op@platform.example")
	if err != nil {
		t.Fatalf("enroll mfa: %v", err)
	}
	if _, err := users.ConfirmMFA(opCtx, uid, security.GenerateTOTP(seed, time.Now()), time.Now()); err != nil {
		t.Fatalf("confirm mfa: %v", err)
	}

	srv := httpx.NewServer(httpx.Deps{
		Store: store, Auth: httpx.NewSessionOrJWT(sessions, nil),
		Users: users, Sessions: sessions, IPAllow: ipallow, Access: access,
		Secrets: backend, Audit: auditLog, Issuer: "Waterfall",
	})

	// Compose the admin handler exactly as cmd/dashboardd does: feature routes behind FeatureChain,
	// everything else on the P0 handler.
	fmux := http.NewServeMux()
	providers.Routes(fmux, providers.Deps{
		Store: store, Audit: auditLog, Auth: httpx.CtxAuthenticator{}, Secrets: backend,
	})
	keys.Routes(fmux, keys.Deps{
		Store: store, Secrets: backend, Audit: auditLog, Auth: httpx.CtxAuthenticator{},
	})
	featureHandler := srv.FeatureChain(fmux)
	adminMux := http.NewServeMux()
	adminMux.Handle("/", srv.Handler())
	for _, p := range []string{"providers", "keys", "key-pools", "key-imports", "bulk-jobs"} {
		adminMux.Handle("/v1/admin/"+p, featureHandler)
		adminMux.Handle("/v1/admin/"+p+"/", featureHandler)
	}

	ts := httptest.NewServer(adminMux)
	defer ts.Close()
	cl := &client{t: t, base: ts.URL}

	// login + mfa -> authenticated operator session with a CSRF token.
	if st, _ := cl.do("POST", "/v1/admin/auth/login", "", `{"email":"op@platform.example","password":"correct horse battery staple"}`); st != 200 {
		t.Fatalf("login = %d, want 200", st)
	}
	st, body := cl.do("POST", "/v1/admin/auth/mfa/verify", "", `{"code":"`+security.GenerateTOTP(seed, time.Now())+`"}`)
	if st != 200 {
		t.Fatalf("mfa/verify = %d %v, want 200", st, body)
	}
	cl.csrf, _ = body["csrf_token"].(string)
	if cl.csrf == "" {
		t.Fatal("mfa/verify did not return csrf_token")
	}

	// (1) Reachability + single-auth: a feature GET routes through FeatureChain to the providers
	// handler and returns the (empty) catalog list, not 401/404.
	st, list := cl.do("GET", "/v1/admin/providers", "", "")
	if st != 200 {
		t.Fatalf("GET /v1/admin/providers = %d %v, want 200 (feature route reachable + authenticated)", st, list)
	}

	// (2) CSRF enforcement on a feature route: mutating request WITHOUT X-CSRF-Token => 403
	// csrf_required, enforced by the shared FeatureChain before the feature handler.
	st, body = cl.doRaw("POST", "/v1/admin/providers", cl.cookie, "" /*no csrf*/, "idem-fw-1",
		`{"id":"hunter","display_name":"Hunter","status":"ACTIVE-CANDIDATE"}`)
	if st != 403 || errCode(body) != "csrf_required" {
		t.Fatalf("feature POST without CSRF = %d %v, want 403 csrf_required", st, body)
	}

	// (3) With a valid CSRF token the same request passes the shared chain (no csrf_required);
	// it reaches the feature handler (create succeeds or a validation/RBAC verdict, never a CSRF block).
	st, body = cl.doRaw("POST", "/v1/admin/providers", cl.cookie, cl.csrf, "idem-fw-2",
		`{"id":"hunter","display_name":"Hunter","status":"ACTIVE-CANDIDATE"}`)
	if st == 403 && errCode(body) == "csrf_required" {
		t.Fatalf("feature POST with valid CSRF still blocked as csrf_required: %v", body)
	}
	if st != 201 && st != 200 {
		t.Fatalf("feature POST with valid CSRF = %d %v, want create success", st, body)
	}
}

// --- P4 approval-gate wiring E2E ---

// gateHdrAuth resolves the Principal from X-Test-* headers, so the gate-wiring test can act as
// distinct operators without the full session/CSRF machinery (this isolates the gate wiring; the
// shared FeatureChain protections are proven by TestDashFeatureWiring).
type gateHdrAuth struct{}

func (gateHdrAuth) Authenticate(r *http.Request) (tenant.Principal, error) {
	return tenant.Principal{
		TenantID: r.Header.Get("X-Test-Tenant"),
		UserID:   r.Header.Get("X-Test-User"),
		Scopes:   []string{"role:" + r.Header.Get("X-Test-Role")},
	}, nil
}

// gateStepUp accepts exactly one X-MFA-Code, so the decision path proves the step-up header is
// threaded without needing a live TOTP seed (matches the approvals package's own harness).
type gateStepUp struct{}

func (gateStepUp) VerifyStepUp(_ context.Context, code string) error {
	if code == "111111" {
		return nil
	}
	return errors.New("bad mfa code")
}

// setupGateSchema rebuilds migrations 0004 + 0005 + 0007 cleanly and grants the non-superuser
// dash_app role, so the whole gate path (provider delete + approval quorum + audit chain) runs
// under FORCE RLS as a non-superuser.
func setupGateSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	// Drop 0007 + 0005 objects first (they depend on 0004's secret_envelopes / providers).
	tryExec(admin, "drop table if exists approval_decisions, approval_requests, approval_policies, "+
		"alert_notifications, alert_events, alert_rules, alert_channels cascade")
	tryExec(admin, "drop view if exists providers_catalog cascade")
	tryExec(admin, "drop table if exists providers, key_pools, provider_keys, key_pool_members, "+
		"key_budgets, key_import_batches, health_schedules, rotation_triggers cascade")

	setupDashSchema(t, admin) // rebuilds 0004 + dash_app + grants on the 0004 tables

	applyGateMigration(t, admin, "../../../migrations/0005_dash_providers_keys.sql")
	mustExec(t, admin, "alter view providers_catalog set (security_invoker = true)")
	const feat0005 = "providers, key_pools, provider_keys, key_pool_members, key_budgets, " +
		"key_import_batches, health_schedules, rotation_triggers"
	mustExec(t, admin, "grant select, insert, update, delete on "+feat0005+" to "+appRole)
	mustExec(t, admin, "grant select on providers_catalog to "+appRole)

	applyGateMigration(t, admin, "../../../migrations/0007_dash_alerts_approvals.sql")
	mustExec(t, admin, "grant select, insert, update, delete on "+
		"approval_policies, approval_requests, approval_decisions to "+appRole)
}

func applyGateMigration(t *testing.T, admin *pg.Conn, path string) {
	t.Helper()
	ddl, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}

// gateReq issues a header-authenticated JSON request and returns (status, decoded body).
func gateReq(t *testing.T, base, method, path, user, role, mfa, idem, body string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Test-Tenant", "platform")
	req.Header.Set("X-Test-User", user)
	req.Header.Set("X-Test-Role", role)
	if mfa != "" {
		req.Header.Set("X-MFA-Code", mfa)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return resp.StatusCode, m
}

// TestDashApprovalGateWiring proves the P4 gate wiring end-to-end (doc 04 §5.3, OI-IP-2): a provider
// DELETE with an approval policy present returns 202 {approval_request_id} instead of an inline
// delete; a distinct operator (four-eyes) approving with X-MFA-Code drives the request to executed,
// the registered Executor runs EXACTLY ONCE, and the provider is then actually deleted. A replayed
// final approval performs no second delete.
func TestDashApprovalGateWiring(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupGateSchema(t, admin)

	// Two DISTINCT platform operators: the requester (issues the delete) and the four-eyes approver.
	requester := newUUID()
	approver := newUUID()
	mustExec(t, admin, `insert into users (id, tenant_id, email, password_hash, role, status) values
		($1,'platform','req@op.example','x','operator','active'),
		($2,'platform','apr@op.example','x','operator','active')`, requester, approver)
	// An explicit provider_delete policy: required=1, approver_role=operator (matches the fail-closed
	// platform default, made explicit so "an approval policy is present").
	mustExec(t, admin, `insert into approval_policies (tenant_id, action_kind, required_approvals, approver_role, expires_after_s)
		values ('platform','provider_delete',1,'operator',86400)`)

	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
	defer pool.Close()
	store := db.New(pool)
	auditLog := audit.New(store)

	// The real service the Executor drives, plus an invocation counter to assert exactly-once.
	provSvc := providers.NewService(providers.Deps{Store: store, Audit: auditLog, Now: time.Now})
	var execCount atomic.Int64
	apprSvc := approvals.NewService(approvals.Config{
		Store: store, Audit: auditLog, Roster: approvals.NewRoster(store), Now: time.Now,
	})
	apprSvc.RegisterExecutor(approvals.ActionProviderDelete, func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			ID string `json:"id"`
		}
		if uerr := json.Unmarshal(payload, &p); uerr != nil {
			return uerr
		}
		execCount.Add(1)
		return provSvc.Delete(ctx, p.ID)
	})

	// Mount exactly as cmd/dashboardd composes them: providers with the Gate wired, approvals surface
	// with the step-up verifier. (Direct mount, not behind FeatureChain, to isolate the gate wiring.)
	mux := http.NewServeMux()
	providers.Routes(mux, providers.Deps{
		Store: store, Audit: auditLog, Auth: gateHdrAuth{}, Gate: apprSvc, Now: time.Now,
	})
	approvals.Routes(mux, approvals.Deps{Service: apprSvc, Auth: gateHdrAuth{}, StepUp: gateStepUp{}})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Create a provider to delete (operator write).
	const provID = "gated-prov"
	if st, body := gateReq(t, ts.URL, "POST", "/v1/admin/providers", requester, "operator", "", "idem-create",
		`{"id":"`+provID+`","display_name":"Gated","status":"ACTIVE-CANDIDATE"}`); st != http.StatusCreated {
		t.Fatalf("create provider = %d %v, want 201", st, body)
	}

	// (1) Gated DELETE: returns 202 {approval_request_id}, NOT an inline delete.
	st, body := gateReq(t, ts.URL, "DELETE", "/v1/admin/providers/"+provID, requester, "operator", "", "idem-del", "")
	if st != http.StatusAccepted {
		t.Fatalf("gated DELETE = %d %v, want 202 (approval required, not inline)", st, body)
	}
	reqID, _ := body["approval_request_id"].(string)
	if reqID == "" {
		t.Fatalf("gated DELETE 202 body missing approval_request_id: %v", body)
	}
	if execCount.Load() != 0 {
		t.Fatalf("Executor ran %d times before quorum, want 0", execCount.Load())
	}
	// Provider still exists (the delete did NOT happen inline).
	if st, _ := gateReq(t, ts.URL, "GET", "/v1/admin/providers/"+provID, requester, "operator", "", "", ""); st != http.StatusOK {
		t.Fatalf("provider GET after 202 = %d, want 200 (must NOT be deleted before quorum)", st)
	}

	// (2) Four-eyes approval by a DISTINCT operator with X-MFA-Code drives it to executed.
	st, body = gateReq(t, ts.URL, "POST", "/v1/admin/approvals/"+reqID+"/approve", approver, "operator", "111111", "idem-appr",
		`{"comment":"approved for deletion"}`)
	if st != http.StatusOK {
		t.Fatalf("approve = %d %v, want 200", st, body)
	}
	if body["status"] != approvals.StatusExecuted {
		t.Fatalf("request status after approval = %v, want executed", body["status"])
	}
	if execCount.Load() != 1 {
		t.Fatalf("Executor ran %d times, want EXACTLY 1", execCount.Load())
	}

	// (3) The provider is now actually deleted (the Executor performed the real delete).
	if st, _ := gateReq(t, ts.URL, "GET", "/v1/admin/providers/"+provID, requester, "operator", "", "", ""); st != http.StatusNotFound {
		t.Fatalf("provider GET after execution = %d, want 404 (executor deleted it)", st)
	}

	// (4) Replay: a further final approval returns the stored result with NO second delete.
	st, _ = gateReq(t, ts.URL, "POST", "/v1/admin/approvals/"+reqID+"/approve", newUUID(), "operator", "111111", "idem-appr2",
		`{"comment":"late approve"}`)
	if st != http.StatusOK {
		t.Fatalf("replay approve = %d, want 200 (terminal, no error)", st)
	}
	if execCount.Load() != 1 {
		t.Fatalf("Executor ran %d times after replay, want still 1", execCount.Load())
	}

	// The platform audit chain (provider create + delete, approval create/execute/approve) is intact.
	ctxPlatform := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "platform", UserID: requester, Scopes: []string{"role:operator"},
	})
	if ok, brokenSeq, verr := auditLog.Verify(ctxPlatform, "platform"); verr != nil || !ok {
		t.Fatalf("platform audit chain verify: ok=%v brokenSeq=%d err=%v", ok, brokenSeq, verr)
	}

	t.Log("PASS: gated provider DELETE -> 202 {approval_request_id}; four-eyes operator + X-MFA-Code -> executed; " +
		"executor ran exactly once; provider actually deleted; replay no-op; audit chain intact")
}
