//go:build integration

// Live-Postgres proof for config versioning (modules 6+7) over migration 0006 under FORCE RLS as a
// NON-superuser role (superusers bypass RLS, proving nothing):
//
//   - TestConfigLifecycleAndRLS: the full draft -> validate (report stored, hash pinned) -> edit
//     (reverts to draft) -> re-validate -> publish -> rollback path; config_active always points at
//     a published version; config_epochs bumped exactly once per publish (acceptance #1). Plus RLS
//     zero-rows cross-tenant on config_versions/config_active/config_epochs/workflow_index/budgets,
//     and the enumerated operator SELECT policies.
//   - TestConcurrentPublishConflict: two goroutines publish the same scope in parallel real
//     transactions; EXACTLY one commits, the loser gets version_conflict; config_active is never
//     observed pointing at a non-validated version (acceptance #2). Run under -race.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package configver_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const cvRole = "dash_cv"

// 0006 tables + 0004 tables the audit path needs.
var cvTables = []string{"config_versions", "config_active", "config_epochs", "workflow_index", "budgets"}
var cvIDTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

// okValidator returns a clean report — the lifecycle tests exercise the engine, not the VR catalog.
type okValidator struct{}

func (okValidator) Validate(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"errors":[],"warnings":[]}`), nil
}

func cvAdminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the configver integration test")
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

func setupConfigSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+cvRole+" cascade")
	tryExec(admin, "drop role if exists "+cvRole)
	tryExec(admin, "drop table if exists "+strings.Join(cvTables, ", ")+" cascade")
	tryExec(admin, "drop view if exists providers_catalog cascade")
	tryExec(admin, "drop table if exists "+strings.Join(cvIDTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")

	// app_current_tenant() lives in migration 0001; 0004 + 0006 policies need it.
	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	applyMigration(t, admin, "../../../migrations/0004_dash_identity_rbac.sql")
	applyMigration(t, admin, "../../../migrations/0006_dash_config_versions.sql")

	mustExec(t, admin, "create role "+cvRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+
		strings.Join(append(append([]string{}, cvTables...), cvIDTables...), ", ")+" to "+cvRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+cvRole)

	// platform is pre-seeded by 0004; add two customer tenants for the RLS + lifecycle paths.
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('acme','Acme','customer','active')`)
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('beta','Beta','customer','active')`)
}

func ctxFor(tenantID, role string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: tenantID, UserID: "00000000-0000-4000-8000-000000000001", Scopes: []string{"role:" + role},
	})
}

func newSvc(store *db.Store) *configver.Service {
	return configver.New(configver.Config{
		Store:      configver.NewPGStore(store),
		Audit:      audit.New(store),
		Validators: map[string]configver.Validator{configver.KindRoutingPolicy: okValidator{}},
	})
}

func appStore(t *testing.T, cfg pg.Config) *db.Store {
	appCfg := cfg
	appCfg.User = cvRole
	pool := pg.NewPool(appCfg, 8)
	t.Cleanup(pool.Close)
	return db.New(pool)
}

// countAs runs a count query with GUCs bound to (tenantID, role) via the RLS tx helper.
func countAs(t *testing.T, store *db.Store, tenantID, role, sql string, args ...any) int {
	t.Helper()
	n := 0
	err := store.Tx(ctxFor(tenantID, role), func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			for _, ch := range *res.Rows[0][0] {
				n = n*10 + int(ch-'0')
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func TestConfigLifecycleAndRLS(t *testing.T) {
	cfg := cvAdminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupConfigSchema(t, admin)

	store := appStore(t, cfg)
	svc := newSvc(store)
	acme := ctxFor("acme", "tenant_admin")

	payload := json.RawMessage(`{"schema_version":1,"thresholds":{"confidence_target":0.9}}`)

	// draft
	v, err := svc.CreateDraft(acme, configver.KindRoutingPolicy, "default", payload)
	if err != nil || v.Status != configver.StatusDraft {
		t.Fatalf("create draft: %+v err=%v", v, err)
	}

	// validate -> validated, report stored, hash pinned
	v, err = svc.Validate(acme, v.ID)
	if err != nil || v.Status != configver.StatusValidated {
		t.Fatalf("validate: %q err=%v", v.Status, err)
	}
	if len(v.PayloadHash) == 0 || len(v.ValidationReport) == 0 {
		t.Fatalf("validate must pin hash + store report")
	}

	// edit -> reverts to draft, hash cleared
	v, err = svc.PatchDraft(acme, v.ID, json.RawMessage(`{"schema_version":1,"thresholds":{"confidence_target":0.8}}`))
	if err != nil || v.Status != configver.StatusDraft || len(v.PayloadHash) != 0 {
		t.Fatalf("patch must revert to draft + clear hash: %q hash=%d err=%v", v.Status, len(v.PayloadHash), err)
	}

	// re-validate -> validated, then publish
	if v, err = svc.Validate(acme, v.ID); err != nil || v.Status != configver.StatusValidated {
		t.Fatalf("re-validate: %q err=%v", v.Status, err)
	}
	v1 := v
	if _, err = svc.Publish(acme, v1.ID, nil); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	assertActivePublished(t, store, "acme", "default", v1.ID)
	if e := epoch(t, store, "acme", configver.KindRoutingPolicy); e != 1 {
		t.Fatalf("epoch after 1 publish = %d, want 1", e)
	}

	// second version -> publish; v1 archived, epoch bumped again (exactly once)
	v2, _ := svc.CreateDraft(acme, configver.KindRoutingPolicy, "default", json.RawMessage(`{"schema_version":1,"thresholds":{"confidence_target":0.7}}`))
	if v2.ParentVersionID != v1.ID {
		t.Fatalf("new draft parent should be active v1, got %q", v2.ParentVersionID)
	}
	v2, _ = svc.Validate(acme, v2.ID)
	if _, err = svc.Publish(acme, v2.ID, nil); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if got, _ := svc.GetVersion(acme, v1.ID); got.Status != configver.StatusArchived {
		t.Fatalf("v1 should be archived, got %q", got.Status)
	}
	assertActivePublished(t, store, "acme", "default", v2.ID)
	if e := epoch(t, store, "acme", configver.KindRoutingPolicy); e != 2 {
		t.Fatalf("epoch after 2 publishes = %d, want 2", e)
	}

	// rollback to v1 -> published again; nothing destroyed
	if _, err = svc.Rollback(acme, configver.KindRoutingPolicy, "default", 1, strp(v2.ID)); err != nil {
		t.Fatalf("rollback to v1: %v", err)
	}
	assertActivePublished(t, store, "acme", "default", v1.ID)
	if e := epoch(t, store, "acme", configver.KindRoutingPolicy); e != 3 {
		t.Fatalf("epoch after rollback = %d, want 3", e)
	}

	// --- RLS: Tenant beta sees 0 of acme's rows on every config table ---
	for _, tbl := range []string{"config_versions", "config_active", "config_epochs", "workflow_index"} {
		if n := countAs(t, store, "beta", "tenant_admin", "select count(*) from "+tbl); n != 0 {
			t.Fatalf("tenant beta saw %d rows of acme's %s (RLS breach)", n, tbl)
		}
	}
	// acme sees its own rows.
	if n := countAs(t, store, "acme", "tenant_admin", "select count(*) from config_versions"); n < 2 {
		t.Fatalf("acme should see its own >=2 config_versions, got %d", n)
	}
	// Operator (platform) may cross-tenant SELECT config_versions/config_active/workflow_index.
	if n := countAs(t, store, "platform", "operator", "select count(*) from config_versions"); n < 2 {
		t.Fatalf("operator cross-tenant read of config_versions = %d, want >=2", n)
	}

	// --- budgets RLS: acme seeds one; beta + operator see 0 (budgets is NOT operator-readable) ---
	mustExec(t, admin, `insert into budgets (tenant_id, scope, scope_key, period, limit_credits)
		values ('acme','tenant','','day', 1000)`)
	if n := countAs(t, store, "acme", "tenant_admin", "select count(*) from budgets"); n != 1 {
		t.Fatalf("acme should see its own budget, got %d", n)
	}
	if n := countAs(t, store, "beta", "tenant_admin", "select count(*) from budgets"); n != 0 {
		t.Fatalf("tenant beta saw %d of acme's budgets (RLS breach)", n)
	}
	if n := countAs(t, store, "platform", "operator", "select count(*) from budgets"); n != 0 {
		t.Fatalf("operator saw %d of acme's budgets (budgets has no operator-read policy)", n)
	}

	// audit chain verifies clean for acme (every lifecycle write appended a row).
	ok, brokenSeq, err := audit.New(store).Verify(acme, "acme")
	if err != nil || !ok {
		t.Fatalf("audit chain verify: ok=%v brokenSeq=%d err=%v", ok, brokenSeq, err)
	}
	t.Log("PASS: lifecycle (draft->validate->edit->revalidate->publish->rollback), epoch accounting, RLS zero-rows, operator read policies")
}

// TestConcurrentPublishConflict races two publishers on one scope: exactly one commits, the loser
// gets version_conflict, and config_active is never left pointing at a non-validated version.
func TestConcurrentPublishConflict(t *testing.T) {
	cfg := cvAdminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupConfigSchema(t, admin)

	store := appStore(t, cfg)
	svc := newSvc(store)
	acme := ctxFor("acme", "tenant_admin")

	// A baseline published version V0 is the common parent for the two racing drafts.
	v0, _ := svc.CreateDraft(acme, configver.KindRoutingPolicy, "default", json.RawMessage(`{"schema_version":1,"v":0}`))
	v0, _ = svc.Validate(acme, v0.ID)
	if _, err := svc.Publish(acme, v0.ID, nil); err != nil {
		t.Fatalf("publish baseline v0: %v", err)
	}

	// Two validated drafts, both parented at V0 -> both expect V0 to be active at publish.
	va, _ := svc.CreateDraft(acme, configver.KindRoutingPolicy, "default", json.RawMessage(`{"schema_version":1,"v":"a"}`))
	va, _ = svc.Validate(acme, va.ID)
	vb, _ := svc.CreateDraft(acme, configver.KindRoutingPolicy, "default", json.RawMessage(`{"schema_version":1,"v":"b"}`))
	vb, _ = svc.Validate(acme, vb.ID)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	ids := []string{va.ID, vb.ID}
	for i := range ids {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = svc.Publish(acme, ids[idx], strp(v0.ID))
		}(i)
	}
	close(start)
	wg.Wait()

	winners, losers := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			winners++
		case isVersionConflict(e):
			losers++
		default:
			t.Fatalf("unexpected publish error: %v", e)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("expected exactly one winner + one 409, got winners=%d losers=%d (%v)", winners, losers, errs)
	}

	// config_active points at a PUBLISHED version (one of va/vb); the loser stays validated.
	activeID := activeVersionID(t, store, "acme", "default")
	if activeID != va.ID && activeID != vb.ID {
		t.Fatalf("config_active points at %q, want one of the racers", activeID)
	}
	if st := statusOf(t, store, activeID); st != configver.StatusPublished {
		t.Fatalf("config_active points at a %q version (must never be non-published/validated)", st)
	}
	// epoch bumped exactly twice total (v0 + one winner), never for the loser.
	if e := epoch(t, store, "acme", configver.KindRoutingPolicy); e != 2 {
		t.Fatalf("epoch after baseline + one winning publish = %d, want 2", e)
	}
	t.Logf("PASS: concurrent publish — 1 winner, 1 version_conflict; config_active=%s status=published", activeID)
}

// --- small assertion helpers (raw reads under the app role) ---

func assertActivePublished(t *testing.T, store *db.Store, tenantID, scope, wantID string) {
	t.Helper()
	got := activeVersionID(t, store, tenantID, scope)
	if got != wantID {
		t.Fatalf("config_active for %s/%s = %q, want %q", tenantID, scope, got, wantID)
	}
	if st := statusOf(t, store, got); st != configver.StatusPublished {
		t.Fatalf("active version %s status = %q, want published", got, st)
	}
}

func activeVersionID(t *testing.T, store *db.Store, tenantID, scope string) string {
	t.Helper()
	var id string
	err := store.Tx(ctxFor(tenantID, "tenant_admin"), func(c *pg.Conn) error {
		res, err := c.QueryParams(`select active_version_id from config_active where scope_key=$1`, scope)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			id = *res.Rows[0][0]
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read config_active: %v", err)
	}
	return id
}

func statusOf(t *testing.T, store *db.Store, id string) string {
	t.Helper()
	var st string
	err := store.Tx(ctxFor("acme", "tenant_admin"), func(c *pg.Conn) error {
		res, err := c.QueryParams(`select status from config_versions where id=$1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			st = *res.Rows[0][0]
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	return st
}

func epoch(t *testing.T, store *db.Store, tenantID, kind string) int {
	t.Helper()
	return countAs(t, store, tenantID, "tenant_admin", `select coalesce(epoch,0) from config_epochs where kind=$1`, kind)
}

func isVersionConflict(err error) bool {
	return errors.Is(err, configver.ErrVersionConflict)
}

func strp(s string) *string { return &s }
