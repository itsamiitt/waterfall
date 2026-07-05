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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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

	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+strings.Join(dashTables, ", ")+" to "+appRole)
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
