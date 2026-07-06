//go:build integration

// Live-Postgres proof for T2 (per-Tenant require_mfa knob, SEC-5) and T5e (recovery-code on
// step-up). Runs as the non-superuser dash_app role under FORCE RLS, via the same httptest server
// and client helpers as dash_integration_test.go. Invoke via scripts/run-rls-test.sh or with
// WATERFALL_PG_DSN set.
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
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/httpx"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// mfaTestDeps bundles the constructed services + server the T2/T5e tests drive.
type mfaTestDeps struct {
	store    *db.Store
	users    *security.Users
	sessions *security.Sessions
	audit    *audit.Log
	server   *httpx.Server
	pool     *pg.Pool
}

// buildMFAServer rebuilds the 0004 schema (+ require_mfa + mfa_used_steps), wires the security
// services and the httpx Server, and returns the pieces the T2/T5e tests share.
func buildMFAServer(t *testing.T, admin *pg.Conn, cfg pg.Config) mfaTestDeps {
	t.Helper()
	setupDashSchema(t, admin)

	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
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
	auditLog := audit.New(store)

	srv := httpx.NewServer(httpx.Deps{
		Store: store, Auth: httpx.NewSessionOrJWT(sessions, nil),
		Users: users, Sessions: sessions, IPAllow: ipallow, Access: access,
		Secrets: backend, Audit: auditLog, Issuer: "Waterfall",
	})
	return mfaTestDeps{store: store, users: users, sessions: sessions, audit: auditLog, server: srv, pool: pool}
}

// enrollUser creates an enrolled MFA user (returns its id, TOTP seed, and recovery codes).
func enrollUser(t *testing.T, users *security.Users, tenantID, email, role, password string) (uid string, seed []byte, recovery []string) {
	t.Helper()
	ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: tenantID, UserID: "seed", Scopes: []string{"role:" + role},
	})
	pwHash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid, err = users.Create(ctx, email, pwHash, role)
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	seed, _, err = users.EnrollMFA(ctx, uid, email)
	if err != nil {
		t.Fatalf("enroll %s: %v", email, err)
	}
	recovery, err = users.ConfirmMFA(ctx, uid, security.GenerateTOTP(seed, time.Now()), time.Now())
	if err != nil {
		t.Fatalf("confirm %s: %v", email, err)
	}
	return uid, seed, recovery
}

// TestDashRequireMFAKnob proves T2/SEC-5 end-to-end: the require_mfa knob is tenant-scoped, gates an
// unenrolled tenant_user's login to mfa_enrollment_required (with /auth/me still 401), lets login
// proceed when off, and the PATCH /settings/mfa-policy handler applies + audits it.
func TestDashRequireMFAKnob(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	d := buildMFAServer(t, admin, cfg)
	defer d.pool.Close()

	// Two tenants (globex exists only to prove cross-tenant writes are rejected).
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values
		('acme','Acme','customer','active'), ('globex','Globex','customer','active')`)

	// acme users: an enrolled tenant_admin (drives the PATCH) and an UNENROLLED tenant_user (gated).
	_, adminSeed, adminRecovery := enrollUser(t, d.users, "acme", "admin@acme.example", "tenant_admin", "correct horse battery staple")
	acmeAdminCtx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "acme", UserID: "admin", Scopes: []string{"role:tenant_admin"},
	})
	pwHash, _ := security.HashPassword("hunter2 hunter2 hunter2")
	if _, err := d.users.Create(acmeAdminCtx, "user@acme.example", pwHash, "tenant_user"); err != nil {
		t.Fatalf("create tenant_user: %v", err)
	}

	tp := security.NewTenantPolicy(d.store)

	// --- (1) TenantPolicy is tenant-scoped: acme admin cannot toggle globex (RLS WITH CHECK). ---
	if err := tp.SetRequireMFA(acmeAdminCtx, "globex", true); err != security.ErrNotFound {
		t.Fatalf("cross-tenant SetRequireMFA = %v, want ErrNotFound (RLS must block)", err)
	}
	if got := scalar(t, admin, `select require_mfa from tenants where id = 'globex'`); got != "f" {
		t.Fatalf("globex require_mfa = %q after blocked cross-tenant write, want f", got)
	}
	// Own-tenant write + read round-trips.
	if err := tp.SetRequireMFA(acmeAdminCtx, "acme", true); err != nil {
		t.Fatalf("own-tenant SetRequireMFA(acme,true): %v", err)
	}
	if req, err := tp.RequireMFA(acmeAdminCtx, "acme"); err != nil || !req {
		t.Fatalf("RequireMFA(acme) = (%v,%v), want (true,nil)", req, err)
	}

	ts := httptest.NewServer(d.server.Handler())
	defer ts.Close()

	// --- (2) require_mfa=true: unenrolled tenant_user login -> mfa_enrollment_required, /auth/me 401. ---
	tu := &client{t: t, base: ts.URL}
	st, body := tu.do("POST", "/v1/admin/auth/login", "", `{"email":"user@acme.example","password":"hunter2 hunter2 hunter2"}`)
	if st != 200 || body["status"] != "mfa_enrollment_required" {
		t.Fatalf("gated login = %d %v, want 200 mfa_enrollment_required", st, body)
	}
	if tu.cookie == "" {
		t.Fatal("gated login did not set a session cookie (session must be started)")
	}
	if st, _ := tu.do("GET", "/v1/admin/auth/me", "", ""); st != 401 {
		t.Fatalf("gated /auth/me = %d, want 401 (unenrolled user in require-MFA tenant must be gated)", st)
	}

	// --- (3) require_mfa=false: login proceeds, /auth/me 200 for the same unenrolled user. ---
	if err := tp.SetRequireMFA(acmeAdminCtx, "acme", false); err != nil {
		t.Fatalf("SetRequireMFA(acme,false): %v", err)
	}
	tu2 := &client{t: t, base: ts.URL}
	st, body = tu2.do("POST", "/v1/admin/auth/login", "", `{"email":"user@acme.example","password":"hunter2 hunter2 hunter2"}`)
	if st != 200 || body["status"] != "ok" {
		t.Fatalf("ungated login = %d %v, want 200 ok", st, body)
	}
	if st, _ := tu2.do("GET", "/v1/admin/auth/me", "", ""); st != 200 {
		t.Fatalf("ungated /auth/me = %d, want 200", st)
	}

	// --- (4) PATCH /settings/mfa-policy: tenant_admin sets the knob with step-up, audited. ---
	fmux := http.NewServeMux()
	fmux.Handle("PATCH /v1/admin/settings/mfa-policy", http.HandlerFunc(d.server.HandleMFAPolicy))
	featureHandler := d.server.FeatureChain(fmux)
	adminMux := http.NewServeMux()
	adminMux.Handle("/", d.server.Handler())
	adminMux.Handle("/v1/admin/settings/mfa-policy", featureHandler)
	ts2 := httptest.NewServer(adminMux)
	defer ts2.Close()

	adm := &client{t: t, base: ts2.URL}
	if st, _ := adm.do("POST", "/v1/admin/auth/login", "", `{"email":"admin@acme.example","password":"correct horse battery staple"}`); st != 200 {
		t.Fatalf("admin login = %d, want 200", st)
	}
	st, vb := adm.do("POST", "/v1/admin/auth/mfa/verify", "", `{"code":"`+security.GenerateTOTP(adminSeed, time.Now())+`"}`)
	if st != 200 {
		t.Fatalf("admin mfa/verify = %d %v, want 200", st, vb)
	}
	adm.csrf, _ = vb["csrf_token"].(string)

	// Missing step-up code => 401 mfa_required.
	if st, eb := patchMFAPolicy(t, ts2.URL, adm.cookie, adm.csrf, "", `{"require_mfa":true}`); st != 401 || errCode(eb) != "mfa_required" {
		t.Fatalf("PATCH without X-MFA-Code = %d %v, want 401 mfa_required", st, eb)
	}
	// Valid step-up via a RECOVERY CODE (T5e) => 200 and require_mfa=true.
	st, pb := patchMFAPolicy(t, ts2.URL, adm.cookie, adm.csrf, adminRecovery[0], `{"require_mfa":true}`)
	if st != 200 || pb["require_mfa"] != true {
		t.Fatalf("PATCH mfa-policy = %d %v, want 200 require_mfa=true", st, pb)
	}
	if got := scalar(t, admin, `select require_mfa from tenants where id = 'acme'`); got != "t" {
		t.Fatalf("acme require_mfa = %q after PATCH, want t", got)
	}

	// --- (5) the change is audited and the chain still verifies. ---
	_, alist := adm.do("GET", "/v1/admin/audit-log", "", "")
	items, _ := alist["items"].([]any)
	found := false
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["action"] == "mfa_policy_update" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit-log missing mfa_policy_update row; items=%v", items)
	}
	if st, vfy := adm.do("GET", "/v1/admin/audit-log/verify", "", ""); st != 200 || vfy["ok"] != true {
		t.Fatalf("audit-log/verify = %d %v, want 200 ok=true", st, vfy)
	}

	t.Log("PASS: require_mfa is tenant-scoped; unenrolled login -> mfa_enrollment_required + /auth/me 401; " +
		"login proceeds when off; PATCH mfa-policy step-up-gated + audited")
}

// TestDashVerifyStepUp proves T5e: security.Users.VerifyStepUp accepts BOTH a fresh TOTP and a valid
// recovery code, each strictly single-use, under a controlled clock.
func TestDashVerifyStepUp(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	d := buildMFAServer(t, admin, cfg)
	defer d.pool.Close()
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('acme','Acme','customer','active')`)

	uid, seed, recovery := enrollUser(t, d.users, "acme", "step@acme.example", "tenant_admin", "correct horse battery staple")
	ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "acme", UserID: uid, Scopes: []string{"role:tenant_admin"},
	})
	now := time.Now()

	// A fresh TOTP is accepted once, then rejected as a replay (mfa_used_steps single-use).
	code := security.GenerateTOTP(seed, now)
	if ok, err := d.users.VerifyStepUp(ctx, uid, code, now); err != nil || !ok {
		t.Fatalf("VerifyStepUp(TOTP) = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, err := d.users.VerifyStepUp(ctx, uid, code, now); err != nil || ok {
		t.Fatalf("VerifyStepUp(TOTP replay) = (%v,%v), want (false,nil) — TOTP must be single-use", ok, err)
	}

	// A recovery code is accepted once, then rejected (used_at single-use).
	if ok, err := d.users.VerifyStepUp(ctx, uid, recovery[0], now); err != nil || !ok {
		t.Fatalf("VerifyStepUp(recovery) = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, err := d.users.VerifyStepUp(ctx, uid, recovery[0], now); err != nil || ok {
		t.Fatalf("VerifyStepUp(recovery replay) = (%v,%v), want (false,nil) — recovery code must be single-use", ok, err)
	}

	// A distinct, still-unused recovery code is independently accepted (proves per-code single-use).
	if ok, err := d.users.VerifyStepUp(ctx, uid, recovery[1], now); err != nil || !ok {
		t.Fatalf("VerifyStepUp(recovery[1]) = (%v,%v), want (true,nil)", ok, err)
	}
	// A garbage code is rejected without error.
	if ok, err := d.users.VerifyStepUp(ctx, uid, "000000", now); err != nil || ok {
		t.Fatalf("VerifyStepUp(bogus) = (%v,%v), want (false,nil)", ok, err)
	}

	t.Log("PASS: VerifyStepUp accepts a fresh TOTP and a valid recovery code, each strictly single-use")
}

// patchMFAPolicy issues a PATCH carrying the session cookie, CSRF token, and X-MFA-Code step-up
// header (the shared client helper does not thread X-MFA-Code).
func patchMFAPolicy(t *testing.T, base, cookie, csrf, mfaCode, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("PATCH", base+"/v1/admin/settings/mfa-policy", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Cookie", "dash_session="+cookie)
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if mfaCode != "" {
		req.Header.Set("X-MFA-Code", mfaCode)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH mfa-policy: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return resp.StatusCode, m
}
