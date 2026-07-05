//go:build integration

// Live-Postgres proof for Provider Management (module 2) over migration 0005 under FORCE RLS as a
// NON-superuser role (superusers bypass RLS, proving nothing):
//
//   - CRUD + every lifecycle action round-trips through the HTTP surface and appends an audit row;
//     List is cursor-paginated and bounded.
//   - Gate G1 / Class P: a customer-tenant Principal sees only the tenant_readable providers_catalog
//     projection (catalog columns; op_state and other platform-only fields absent) and NEVER a
//     platform_only row; archive soft-deletes; delete hard-removes.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package providers_test

import (
	"context"
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
	"github.com/enrichment/waterfall/internal/dash/providers"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const provRole = "dash_prov"

// 0005 tables (providers first; the rest reference it / secret_envelopes).
var provTables = []string{
	"key_pool_members", "key_budgets", "provider_keys", "key_pools",
	"key_import_batches", "health_schedules", "rotation_triggers", "providers",
}

// 0004 tables the audit path + FKs need.
var idTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the providers integration test")
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

// setupProviderSchema rebuilds migrations 0004+0005 cleanly and provisions the non-superuser
// provRole. The providers_catalog view is created here by a superuser, so security_invoker is
// enabled to make its scan of providers respect the invoking (non-superuser) role's RLS — in
// production the view is owned by a non-superuser app role and FORCE RLS enforces the same
// (migration 0005 comment).
func setupProviderSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+provRole+" cascade")
	tryExec(admin, "drop role if exists "+provRole)
	tryExec(admin, "drop view if exists providers_catalog cascade")
	tryExec(admin, "drop table if exists "+strings.Join(provTables, ", ")+" cascade")
	tryExec(admin, "drop table if exists "+strings.Join(idTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")

	// app_current_tenant() lives in migration 0001; both 0004 and 0005 policies need it.
	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	applyMigration(t, admin, "../../../migrations/0004_dash_identity_rbac.sql")
	applyMigration(t, admin, "../../../migrations/0005_dash_providers_keys.sql")

	// Make the catalog view enforce the invoker's RLS (see doc comment above).
	mustExec(t, admin, "alter view providers_catalog set (security_invoker = true)")

	// Non-superuser app role + grants (providers CRUD, catalog SELECT, audit append path).
	mustExec(t, admin, "create role "+provRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+
		strings.Join(append(append([]string{}, provTables...), idTables...), ", ")+" to "+provRole)
	mustExec(t, admin, "grant select on providers_catalog to "+provRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+provRole)

	// The 'platform' tenant is pre-seeded by 0004; add a customer tenant for the RLS read path.
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('acme','Acme','customer','active')`)
}

func applyMigration(t *testing.T, admin *pg.Conn, path string) {
	t.Helper()
	ddl, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}

// opAuth binds the operator Principal (tenant='platform') for the HTTP surface.
type opAuth struct{}

func (opAuth) Authenticate(*http.Request) (tenant.Principal, error) {
	return tenant.Principal{TenantID: "platform", UserID: "00000000-0000-4000-8000-000000000001", Scopes: []string{"role:operator"}}, nil
}

func opCtx() context.Context {
	return tenant.WithPrincipal(context.Background(),
		tenant.Principal{TenantID: "platform", UserID: "00000000-0000-4000-8000-000000000001", Scopes: []string{"role:operator"}})
}

func tenantCtx() context.Context {
	return tenant.WithPrincipal(context.Background(),
		tenant.Principal{TenantID: "acme", UserID: "00000000-0000-4000-8000-000000000009", Scopes: []string{"role:tenant_user"}})
}

func TestProvidersLifecycleAndRLS(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupProviderSchema(t, admin)

	appCfg := cfg
	appCfg.User = provRole
	pool := pg.NewPool(appCfg, 8)
	defer pool.Close()
	store := db.New(pool)
	auditLog := audit.New(store)

	deps := providers.Deps{Store: store, Audit: auditLog, Auth: opAuth{}, Now: time.Now}
	svc := providers.NewService(deps)
	mux := http.NewServeMux()
	providers.Routes(mux, deps)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cl := &client{t: t, base: ts.URL}

	// --- create (config-first onboarding) ---
	create := `{"id":"hunter","display_name":"Hunter","category":"email_finder",
		"base_url":"https://api.hunter.io","auth_scheme":"api-key-header","auth_header":"X-API-Key",
		"region":["us","eu"],"tags":["email"],
		"capabilities":[{"field":"work_email","cost_credits":1,"expected_confidence":0.9}]}`
	st, body := cl.do("POST", "/v1/admin/providers", "c1", create)
	if st != 201 || body["status"] != providers.StatusDeprioritized {
		t.Fatalf("create = %d %v, want 201 DEPRIORITIZED", st, body)
	}
	if body["effective_available"] != false || body["unavailable_reason"] != providers.ReasonStatusDeprioritized {
		t.Fatalf("new provider must be unavailable(status_deprioritized): %v", body)
	}

	// --- get ---
	if st, _ := cl.do("GET", "/v1/admin/providers/hunter", "", ""); st != 200 {
		t.Fatalf("get = %d, want 200", st)
	}

	// --- patch: approve + promote to ACTIVE-CANDIDATE ---
	st, _ = cl.do("PATCH", "/v1/admin/providers/hunter", "p1",
		`{"status":"ACTIVE-CANDIDATE","compliance_review_status":"approved","priority":10}`)
	if st != 200 {
		t.Fatalf("patch = %d, want 200", st)
	}

	// --- lifecycle actions round-trip ---
	st, body = cl.do("POST", "/v1/admin/providers/hunter/enable", "a1", `{"reason":"go live"}`)
	if st != 200 || body["op_state"] != providers.OpEnabled || body["effective_available"] != true {
		t.Fatalf("enable = %d %v, want enabled+available", st, body)
	}
	for _, act := range []string{"pause", "maintenance", "disable", "enable"} {
		if st, m := cl.do("POST", "/v1/admin/providers/hunter/"+act, "op-"+act, ``); st != 200 {
			t.Fatalf("%s = %d %v, want 200", act, st, m)
		}
	}
	// Re-enable (no-op transition) => 422.
	if st, _ := cl.do("POST", "/v1/admin/providers/hunter/enable", "reenable", ``); st != 422 {
		t.Fatalf("re-enable no-op transition = %d, want 422", st)
	}

	// refresh-metadata (stub) + sync-credits (manual) + health-check + test + benchmark.
	if st, _ := cl.do("POST", "/v1/admin/providers/hunter/refresh-metadata", "rm", ``); st != 200 {
		t.Fatal("refresh-metadata not 200")
	}
	st, body = cl.do("POST", "/v1/admin/providers/hunter/sync-credits", "sc", `{"credits_remaining":4200}`)
	if st != 200 || body["credits_remaining"] == nil {
		t.Fatalf("sync-credits(manual) = %d %v", st, body)
	}
	if st, _ := cl.do("POST", "/v1/admin/providers/hunter/health-check", "hc", ``); st != 200 {
		t.Fatal("health-check not 200")
	}
	// test/benchmark: no key resolver wired => typed no-key result, still 200 (never a crash).
	if st, m := cl.do("POST", "/v1/admin/providers/hunter/test", "tst", ``); st != 200 {
		t.Fatalf("test = %d %v, want 200", st, m)
	}
	if st, _ := cl.do("POST", "/v1/admin/providers/hunter/benchmark", "bm", ``); st != 200 {
		t.Fatal("benchmark not 200")
	}

	// --- duplicate ---
	st, body = cl.do("POST", "/v1/admin/providers/hunter/duplicate", "dup", `{"id":"hunter-2"}`)
	if st != 201 || body["id"] != "hunter-2" || body["status"] != providers.StatusDeprioritized {
		t.Fatalf("duplicate = %d %v, want 201 fresh draft", st, body)
	}

	// --- list: bounded + cursor-paginated ---
	st, body = cl.do("GET", "/v1/admin/providers?limit=1", "", "")
	if st != 200 {
		t.Fatalf("list = %d", st)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("limit=1 returned %d items", len(items))
	}
	nc, _ := body["next_cursor"].(string)
	if nc == "" {
		t.Fatal("expected a next_cursor with 2 providers and limit=1")
	}
	st, body = cl.do("GET", "/v1/admin/providers?limit=1&cursor="+nc, "", "")
	if items2, _ := body["items"].([]any); st != 200 || len(items2) != 1 {
		t.Fatalf("second page = %d %v", st, body)
	}
	// Over-cap limit rejected.
	if st, _ := cl.do("GET", "/v1/admin/providers?limit=5000", "", ""); st != 400 {
		t.Fatal("limit=5000 not 400")
	}

	// --- RLS: seed a platform_only provider, then read as a customer tenant ---
	if st, _ := cl.do("POST", "/v1/admin/providers", "po",
		`{"id":"secretco","display_name":"Secret","visibility":"platform_only"}`); st != 201 {
		t.Fatal("create platform_only provider failed")
	}

	// Tenant sees the tenant_readable catalog projection only (no op_state / base_url columns).
	tv, err := svc.Get(tenantCtx(), "hunter")
	if err != nil {
		t.Fatalf("tenant get hunter: %v", err)
	}
	if tv.OpState != "" {
		t.Errorf("tenant projection leaked op_state=%q (must be absent)", tv.OpState)
	}
	if tv.BaseURL != "" {
		t.Errorf("tenant projection leaked base_url=%q", tv.BaseURL)
	}
	if len(tv.Capabilities) == 0 {
		t.Errorf("tenant projection dropped capabilities")
	}
	// Tenant NEVER sees a platform_only row.
	if _, err := svc.Get(tenantCtx(), "secretco"); err != providers.ErrNotFound {
		t.Fatalf("tenant get platform_only = %v, want ErrNotFound", err)
	}
	tlist, _, err := svc.List(tenantCtx(), providers.Filter{}, db.Cursor{}, 50)
	if err != nil {
		t.Fatalf("tenant list: %v", err)
	}
	for _, p := range tlist {
		if p.ID == "secretco" {
			t.Fatal("tenant list leaked a platform_only provider")
		}
		if p.OpState != "" {
			t.Errorf("tenant list row leaked op_state for %s", p.ID)
		}
	}

	// --- archive (soft) then delete (hard) ---
	st, body = cl.do("POST", "/v1/admin/providers/hunter-2/archive", "arch", ``)
	if st != 200 || body["archived_at"] == nil {
		t.Fatalf("archive = %d %v, want archived_at set", st, body)
	}
	if st, _ := cl.do("DELETE", "/v1/admin/providers/hunter-2", "del", ``); st != 200 {
		t.Fatal("delete not 200")
	}
	if _, err := svc.Get(opCtx(), "hunter-2"); err != providers.ErrNotFound {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}

	// --- audit: mutations appended rows under the platform chain, and it verifies clean ---
	n := scalar(t, admin, "select count(*) from audit_log where object_kind='providers'")
	if n == "0" || n == "" {
		t.Fatalf("expected provider audit rows, got %q", n)
	}
	ok, brokenSeq, err := auditLog.Verify(opCtx(), "platform")
	if err != nil || !ok {
		t.Fatalf("audit chain verify: ok=%v brokenSeq=%d err=%v", ok, brokenSeq, err)
	}

	t.Logf("PASS: providers CRUD+actions+audit(%s rows); RLS catalog projection + platform_only invisible; archive/delete", n)
}

// --- HTTP client helper ---

type client struct {
	t    *testing.T
	base string
}

func (c *client) do(method, path, idemKey, body string) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
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
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(strings.TrimSpace(string(raw))) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return resp.StatusCode, m
}
