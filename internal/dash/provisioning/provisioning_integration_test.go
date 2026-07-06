//go:build integration

// Live-Postgres proof for operator Tenant provisioning (SEC-3, ADR-0021) over the migration-0004
// identity schema plus the migration-0012 tenant_invites table, run under FORCE ROW LEVEL SECURITY
// as the NON-superuser dash_app role (superusers bypass RLS, proving nothing):
//
//   - TestProvisioningTenantAndInvite — an operator provisions Tenant 'acme' + its first
//     tenant_admin + a one-time token (ADR-0021 target-Tenant-bound INSERT, no BYPASSRLS); the HTTP
//     handler admits an operator (201) and rejects a tenant_admin (403 forbidden); the public
//     accept-invite sets the admin's password (VerifyPassword succeeds) and is single-use (2nd =>
//     409); tenant_invites zero-rows cross-Tenant and cross-Tenant INSERT is blocked by WITH CHECK.
//   - TestProvisioningInviteSingleUseRace — two concurrent accepts of one token: exactly one wins,
//     the other gets ErrInviteUsed (the -race guard on the single-use claim).
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package provisioning_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/provisioning"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_app"

// dashTables is every table created by migration 0004 (dropped/rebuilt so the setup is independent
// of migration-tracking state left by sibling integration packages in the shared database).
var dashTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the provisioning integration tests")
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

func scalar(t *testing.T, c *pg.Conn, sql string, args ...any) string {
	t.Helper()
	res, err := c.QueryParams(sql, args...)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return ""
	}
	return *res.Rows[0][0]
}

// setupSchema rebuilds migration 0004 cleanly, adds the migration-0012 tenant_invites table (its
// subset — 0012 also touches bulk_jobs/cost_rollup_1d, which a 0004-only schema does not have), and
// provisions the non-superuser dash_app role with the grants the provisioning path needs.
func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop table if exists tenant_invites cascade")
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

	// tenant_invites (migration 0012, SEC-3/ADR-0021): Class T, tenant-isolated, token_hash bytea.
	mustExec(t, admin, `create table tenant_invites (
		id         uuid primary key,
		tenant_id  text not null references tenants(id),
		email      text not null,
		role       text not null check (role in ('tenant_admin','tenant_user')),
		token_hash bytea not null,
		expires_at timestamptz not null,
		used_at    timestamptz,
		created_by uuid,
		created_at timestamptz not null default now())`)
	mustExec(t, admin, "create index tenant_invites_token_idx on tenant_invites (token_hash)")
	mustExec(t, admin, "alter table tenant_invites enable row level security")
	mustExec(t, admin, "alter table tenant_invites force row level security")
	mustExec(t, admin, `create policy tenant_invites_tenant_isolation on tenant_invites
		using (tenant_id = app_current_tenant()) with check (tenant_id = app_current_tenant())`)

	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+
		"tenants, users, audit_log, audit_chain_heads, tenant_invites to "+appRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq to "+appRole)
}

func newStore(t *testing.T, cfg pg.Config) (*db.Store, func()) {
	t.Helper()
	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
	return db.New(pool), func() { pool.Close() }
}

// --- HTTP passthrough auth + step-up (isolates provisioning's own RBAC/idempotency/step-up; the
// shared FeatureChain protections are proven by the e2e suite) ---

type hdrAuth struct{}

func (hdrAuth) Authenticate(r *http.Request) (tenant.Principal, error) {
	return tenant.Principal{
		TenantID: r.Header.Get("X-Test-Tenant"),
		UserID:   r.Header.Get("X-Test-User"),
		Scopes:   []string{"role:" + r.Header.Get("X-Test-Role")},
	}, nil
}

type fixedStepUp struct{}

func (fixedStepUp) VerifyStepUp(_ context.Context, code string) error {
	if code == "111111" {
		return nil
	}
	return errors.New("bad mfa code")
}

func httpReq(t *testing.T, base, method, path string, hdr map[string]string, body string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
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

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// newUUID mints an RFC 4122 v4 uuid for test fixtures (operator/actor ids).
func newUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
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

// jsonStr encodes s as a JSON string literal (quotes + escaping) for inline request bodies.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestProvisioningTenantAndInvite(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	store, closeStore := newStore(t, cfg)
	defer closeStore()
	auditLog := audit.New(store)
	svc := provisioning.NewService(store, auditLog, nil)

	opID := newUUID(t)
	opCtx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "platform", UserID: opID, Scopes: []string{"role:operator"},
	})

	// --- (1) operator provisions Tenant 'acme' + first admin via the service ---
	tid, token, err := svc.ProvisionTenant(opCtx, provisioning.ProvisionRequest{
		ID: "acme", Name: "Acme Inc", PlanTier: "pro", AdminEmail: "admin@acme.example",
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if tid != "acme" || token == "" {
		t.Fatalf("ProvisionTenant = (%q, token=%q), want acme + non-empty token", tid, token)
	}
	if !strings.HasPrefix(token, "acme|") {
		t.Fatalf("invite token missing tenant routing prefix: %q", token)
	}
	// Verify the rows as superuser (bypasses RLS): customer Tenant, invited admin, one live invite.
	if got := scalar(t, admin, `select kind||'/'||status||'/'||coalesce(plan_tier,'') from tenants where id='acme'`); got != "customer/active/pro" {
		t.Fatalf("tenants row = %q, want customer/active/pro", got)
	}
	if got := scalar(t, admin, `select role||'/'||status||'/'||(password_hash='') from users where tenant_id='acme' and lower(email)='admin@acme.example'`); got != "tenant_admin/invited/true" {
		t.Fatalf("users row = %q, want tenant_admin/invited/true (empty password)", got)
	}
	if got := scalar(t, admin, `select count(*) from tenant_invites where tenant_id='acme' and role='tenant_admin' and used_at is null and expires_at > now()`); got != "1" {
		t.Fatalf("live invite count = %q, want 1", got)
	}

	// Re-provisioning the same slug is a conflict.
	if _, _, err := svc.ProvisionTenant(opCtx, provisioning.ProvisionRequest{
		ID: "acme", Name: "Acme Again", AdminEmail: "x@acme.example",
	}); !errors.Is(err, provisioning.ErrTenantExists) {
		t.Fatalf("re-provision acme err = %v, want ErrTenantExists", err)
	}
	// A bad slug is rejected before any write.
	if _, _, err := svc.ProvisionTenant(opCtx, provisioning.ProvisionRequest{
		ID: "Bad_Slug", Name: "X", AdminEmail: "x@x.io",
	}); !errors.Is(err, provisioning.ErrInvalidSlug) {
		t.Fatalf("bad-slug err = %v, want ErrInvalidSlug", err)
	}

	// --- (2) HTTP handler RBAC: operator provisions 'globex' (201); tenant_admin is forbidden ---
	mux := http.NewServeMux()
	deps := provisioning.Deps{
		Store: store, Audit: auditLog, Auth: hdrAuth{}, StepUp: fixedStepUp{},
	}
	provisioning.Routes(mux, deps)       // feature surface (operator provision write)
	provisioning.PublicRoutes(mux, deps) // pre-session accept-invite (mounted publicly in dashboardd)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	opHdr := func(idem string) map[string]string {
		return map[string]string{
			"X-Test-Tenant": "platform", "X-Test-User": opID, "X-Test-Role": "operator",
			"X-MFA-Code": "111111", "Idempotency-Key": idem,
		}
	}
	st, body := httpReq(t, ts.URL, "POST", "/v1/admin/tenants", opHdr("idem-globex"),
		`{"id":"globex","name":"Globex","plan_tier":"free","admin_email":"boss@globex.example"}`)
	if st != http.StatusCreated {
		t.Fatalf("operator POST /tenants = %d %v, want 201", st, body)
	}
	if body["tenant_id"] != "globex" || body["invite_token"] == "" {
		t.Fatalf("provision body = %v, want tenant_id=globex + invite_token", body)
	}

	// tenant_admin cannot provision -> 403 forbidden.
	st, body = httpReq(t, ts.URL, "POST", "/v1/admin/tenants", map[string]string{
		"X-Test-Tenant": "acme", "X-Test-User": newUUID(t), "X-Test-Role": "tenant_admin",
		"X-MFA-Code": "111111", "Idempotency-Key": "idem-ta",
	}, `{"id":"initech","name":"Initech","admin_email":"a@initech.example"}`)
	if st != http.StatusForbidden || errCode(body) != "forbidden" {
		t.Fatalf("tenant_admin POST /tenants = %d %v, want 403 forbidden", st, body)
	}
	// Missing step-up code -> 401 mfa_required; missing Idempotency-Key -> 400.
	st, body = httpReq(t, ts.URL, "POST", "/v1/admin/tenants", map[string]string{
		"X-Test-Tenant": "platform", "X-Test-User": opID, "X-Test-Role": "operator", "Idempotency-Key": "idem-nomfa",
	}, `{"id":"nomfa","name":"N","admin_email":"a@n.io"}`)
	if st != http.StatusUnauthorized || errCode(body) != "mfa_required" {
		t.Fatalf("no-stepup POST /tenants = %d %v, want 401 mfa_required", st, body)
	}
	st, body = httpReq(t, ts.URL, "POST", "/v1/admin/tenants", map[string]string{
		"X-Test-Tenant": "platform", "X-Test-User": opID, "X-Test-Role": "operator", "X-MFA-Code": "111111",
	}, `{"id":"noidem","name":"N","admin_email":"a@n.io"}`)
	if st != http.StatusBadRequest || errCode(body) != "missing_idempotency_key" {
		t.Fatalf("no-idem POST /tenants = %d %v, want 400 missing_idempotency_key", st, body)
	}

	// --- (3) PUBLIC accept-invite (NO auth headers): sets the acme admin's password ---
	const adminPW = "correct-horse-battery-staple"
	st, body = httpReq(t, ts.URL, "POST", "/v1/admin/auth/accept-invite", nil,
		`{"token":`+jsonStr(token)+`,"password":`+jsonStr(adminPW)+`}`)
	if st != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("public accept-invite = %d %v, want 200 ok", st, body)
	}
	// The password is now set and verifies; the user is active.
	hash := scalar(t, admin, `select password_hash from users where tenant_id='acme' and lower(email)='admin@acme.example'`)
	if !security.VerifyPassword(adminPW, hash) {
		t.Fatalf("VerifyPassword failed after accept-invite (hash=%q)", hash)
	}
	if got := scalar(t, admin, `select status from users where tenant_id='acme' and lower(email)='admin@acme.example'`); got != "active" {
		t.Fatalf("user status after accept = %q, want active", got)
	}
	if got := scalar(t, admin, `select (used_at is not null) from tenant_invites where tenant_id='acme'`); got != "t" {
		t.Fatalf("invite used_at not set after accept")
	}

	// --- (4) single-use: a second accept with the same token -> 409 conflict ---
	st, body = httpReq(t, ts.URL, "POST", "/v1/admin/auth/accept-invite", nil,
		`{"token":`+jsonStr(token)+`,"password":`+jsonStr(adminPW)+`}`)
	if st != http.StatusConflict || errCode(body) != "conflict" {
		t.Fatalf("second accept-invite = %d %v, want 409 conflict", st, body)
	}
	// A garbage token is a uniform 404 (existence never disclosed).
	st, body = httpReq(t, ts.URL, "POST", "/v1/admin/auth/accept-invite", nil,
		`{"token":"acme|deadbeef","password":`+jsonStr(adminPW)+`}`)
	if st != http.StatusNotFound || errCode(body) != "not_found" {
		t.Fatalf("garbage-token accept = %d %v, want 404 not_found", st, body)
	}

	// --- (5) RLS: tenant_invites zero-rows + WITH CHECK block cross-Tenant (as dash_app) ---
	appCfg := cfg
	appCfg.User = appRole
	raw, err := pg.Connect(appCfg)
	if err != nil {
		t.Fatalf("connect %s: %v", appRole, err)
	}
	defer raw.Close()
	set := func(tenantID, role string) {
		mustExec(t, raw, `select set_config('app.current_tenant', $1, false)`, tenantID)
		mustExec(t, raw, `select set_config('app.current_role', $1, false)`, role)
	}
	// Bound to 'globex', none of 'acme's invites/users/tenant are visible.
	set("globex", "tenant_admin")
	for _, tbl := range []string{"tenant_invites", "users"} {
		if got := scalar(t, raw, `select count(*) from `+tbl+` where tenant_id = 'acme'`); got != "0" {
			t.Fatalf("cross-Tenant %s from globex = %s rows, want 0", tbl, got)
		}
	}
	if got := scalar(t, raw, `select count(*) from tenants where id = 'acme'`); got != "0" {
		t.Fatalf("cross-Tenant tenants from globex = %s rows, want 0", got)
	}
	// A cross-Tenant tenant_invites INSERT (bound globex, stamped acme) is blocked by WITH CHECK.
	if err := raw.Exec(`insert into tenant_invites (id, tenant_id, email, role, token_hash, expires_at)
		values ('` + newUUID(t) + `','acme','evil@x','tenant_admin','\x01'::bytea, now()+interval '1 hour')`); err == nil {
		t.Fatal("cross-Tenant tenant_invites INSERT succeeded but WITH CHECK must reject it")
	}
	// Even an operator binding sees zero of acme's invites (no operator read policy on tenant_invites).
	set("globex", "operator")
	if got := scalar(t, raw, `select count(*) from tenant_invites where tenant_id = 'acme'`); got != "0" {
		t.Fatalf("operator cross-Tenant tenant_invites = %s rows, want 0 (no operator policy)", got)
	}

	// The new Tenant's audit chain (tenant_provisioned + invite_accepted) verifies clean.
	acmeCtx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "acme", UserID: opID, Scopes: []string{"role:tenant_admin"},
	})
	if ok, brokenSeq, verr := auditLog.Verify(acmeCtx, "acme"); verr != nil || !ok {
		t.Fatalf("acme audit chain verify: ok=%v brokenSeq=%d err=%v", ok, brokenSeq, verr)
	}

	t.Log("PASS: operator provisions acme+admin+token (ADR-0021); operator 201 / tenant_admin 403; " +
		"public accept-invite sets password (VerifyPassword ok); single-use 409; tenant_invites RLS zero-rows; audit verifies")
}

// TestProvisioningInviteSingleUseRace fires two concurrent AcceptInvite calls on one token and
// asserts exactly one wins (the other gets ErrInviteUsed) — the -race guard on the single-use
// UPDATE ... WHERE used_at IS NULL claim.
func TestProvisioningInviteSingleUseRace(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	store, closeStore := newStore(t, cfg)
	defer closeStore()
	svc := provisioning.NewService(store, audit.New(store), nil)

	opCtx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "platform", UserID: newUUID(t), Scopes: []string{"role:operator"},
	})
	_, token, err := svc.ProvisionTenant(opCtx, provisioning.ProvisionRequest{
		ID: "raceco", Name: "Race Co", AdminEmail: "admin@raceco.example",
	})
	if err != nil {
		t.Fatalf("ProvisionTenant raceco: %v", err)
	}

	const pw = "race-condition-password"
	var wg sync.WaitGroup
	results := make([]error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = svc.AcceptInvite(context.Background(), token, pw)
		}(i)
	}
	close(start)
	wg.Wait()

	success, used := 0, 0
	for _, e := range results {
		switch {
		case e == nil:
			success++
		case errors.Is(e, provisioning.ErrInviteUsed):
			used++
		default:
			t.Fatalf("unexpected AcceptInvite error: %v", e)
		}
	}
	if success != 1 || used != 1 {
		t.Fatalf("concurrent accepts: success=%d used=%d, want exactly 1 each", success, used)
	}
	// The winner's password is set and the invite is durably consumed.
	hash := scalar(t, admin, `select password_hash from users where tenant_id='raceco'`)
	if !security.VerifyPassword(pw, hash) {
		t.Fatal("winner's password did not verify after the race")
	}
	if got := scalar(t, admin, `select (used_at is not null) from tenant_invites where tenant_id='raceco'`); got != "t" {
		t.Fatal("invite not marked used after the race")
	}
	t.Log("PASS: concurrent accept-invite is single-use (exactly one winner; -race clean)")
}
