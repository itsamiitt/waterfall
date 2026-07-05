//go:build integration

// Live-Postgres proof for the approval-quorum engine (module: approvals) over migrations 0004+0007
// under FORCE ROW LEVEL SECURITY as a NON-superuser role (superusers bypass RLS, proving nothing):
//
//   - TestApprovalsExactlyOnce (acceptance #2, run under -race): a fail-closed gate creates a pinned
//     request with required_approvals=1 even with NO approval_policies row; 10 concurrent final
//     approvers race the quorum, and the registered Executor runs EXACTLY ONCE (a counter increments
//     to 1); the pinned payload bytes are what execute; replaying a final approval returns the stored
//     result with no second effect.
//   - TestApprovalsNegativeDecisions (acceptance #3, table-driven over the HTTP surface): four-eyes
//     (requester approving own => 403), approver lacking approver_role => 403, missing/invalid
//     X-MFA-Code => 401, decision on an expired request => 409; plus the §9.1 deadlock guard
//     (a tenant whose only eligible approver is the requester => the gate refuses, never parks).
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package approvals_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/approvals"
	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const apprRole = "dash_appr"

// 0007 tables (approval_decisions references approval_requests; alert_* precede for a clean drop).
var apprTables = []string{
	"approval_decisions", "approval_requests", "approval_policies",
	"alert_notifications", "alert_events", "alert_rules", "alert_channels",
}

// 0004 tables the audit path + roster query need.
var apprIDTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the approvals integration test")
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

// setupApprovalSchema rebuilds migrations 0004+0007 cleanly and provisions the non-superuser
// apprRole, so every assertion runs under FORCE RLS as a non-superuser.
func setupApprovalSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+apprRole+" cascade")
	tryExec(admin, "drop role if exists "+apprRole)
	tryExec(admin, "drop table if exists "+strings.Join(apprTables, ", ")+" cascade")
	tryExec(admin, "drop table if exists "+strings.Join(apprIDTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")

	// app_current_tenant() lives in migration 0001; both 0004 and 0007 policies need it.
	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	applyMigration(t, admin, "../../../migrations/0004_dash_identity_rbac.sql")
	applyMigration(t, admin, "../../../migrations/0007_dash_alerts_approvals.sql")

	mustExec(t, admin, "create role "+apprRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+
		strings.Join(append(append([]string{}, apprTables...), apprIDTables...), ", ")+" to "+apprRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+apprRole)

	// platform is pre-seeded by 0004; add a customer tenant for the RLS + lifecycle paths.
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('acme','Acme','customer','active')`)
}

func appStore(t *testing.T, cfg pg.Config) *db.Store {
	appCfg := cfg
	appCfg.User = apprRole
	pool := pg.NewPool(appCfg, 16)
	t.Cleanup(pool.Close)
	return db.New(pool)
}

func ctxFor(tenantID, userID, role string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: tenantID, UserID: userID, Scopes: []string{"role:" + role},
	})
}

func newUUIDv4() string {
	// A distinct v4 uuid per approver (approval_decisions.approver_user_id has no FK to users, so
	// synthetic ids are fine for the quorum race).
	var b [16]byte
	for i := range b {
		b[i] = byte(time.Now().UnixNano() >> (i % 8 * 8))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	// mix in a monotonically-increasing counter so ids are unique within a tight loop
	n := uuidCounter.Add(1)
	b[0], b[1] = byte(n), byte(n>>8)
	return hex4(b)
}

var uuidCounter atomic.Int64

func hex4(b [16]byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 0, 36)
	emit := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			out = append(out, hexd[b[i]>>4], hexd[b[i]&0x0f])
		}
	}
	emit(0, 4)
	out = append(out, '-')
	emit(4, 6)
	out = append(out, '-')
	emit(6, 8)
	out = append(out, '-')
	emit(8, 10)
	out = append(out, '-')
	emit(10, 16)
	return string(out)
}

var errNoRequestID = errors.New("executor: no request id in ctx")

// TestApprovalsExactlyOnce is acceptance #2: fail-closed gate + exactly-once execution under a
// 10-way concurrent quorum race + payload pinning + replay. Run under -race.
func TestApprovalsExactlyOnce(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupApprovalSchema(t, admin)

	store := appStore(t, cfg)

	var counter atomic.Int64
	var mu sync.Mutex
	var seenPayload []byte
	svc := approvals.NewService(approvals.Config{Store: store, Audit: audit.New(store), Now: time.Now})
	svc.RegisterExecutor(approvals.ActionKeyBulkDelete, func(ctx context.Context, payload json.RawMessage) error {
		if _, ok := approvals.RequestIDFromContext(ctx); !ok {
			return errNoRequestID // proves the request id is threaded for the executor's Idempotency-Key
		}
		counter.Add(1)
		mu.Lock()
		seenPayload = append([]byte(nil), payload...)
		mu.Unlock()
		return nil
	})

	requester := newUUIDv4()
	ctxReq := ctxFor("acme", requester, "tenant_admin")

	// Fail-closed gate: NO approval_policies row exists, yet Check gates (proceed=false) with the
	// built-in default (required_approvals=1) and pins the exact payload bytes.
	pinned := json.RawMessage(`{"ids":["k1","k2"],"pinned":true}`)
	proceed, reqID, err := svc.Check(ctxReq, approvals.ActionKeyBulkDelete, pinned)
	if err != nil || proceed || reqID == "" {
		t.Fatalf("Check (fail-closed) = (proceed=%v id=%q err=%v), want (false, <id>, nil)", proceed, reqID, err)
	}
	got, err := svc.GetRequest(ctxReq, reqID)
	if err != nil || got.Status != approvals.StatusPending || got.RequiredApprovals != 1 {
		t.Fatalf("request after Check = %+v err=%v, want pending required=1", got, err)
	}

	// 10 concurrent DISTINCT final approvers race the required=1 quorum.
	const n = 10
	ctxDecide := ctxFor("acme", "sweeper", "tenant_admin") // ctx supplies RLS tenant only
	approvers := make([]string, n)
	for i := range approvers {
		approvers[i] = newUUIDv4()
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = svc.Approve(ctxDecide, reqID, approvers[idx], "tenant_admin", "concurrent approve", true)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("approver %d returned error: %v", i, e)
		}
	}
	if c := counter.Load(); c != 1 {
		t.Fatalf("Executor ran %d times, want EXACTLY 1 (exactly-once breach)", c)
	}

	fin, err := svc.GetRequest(ctxReq, reqID)
	if err != nil || fin.Status != approvals.StatusExecuted {
		t.Fatalf("final request status = %q err=%v, want executed", fin.Status, err)
	}
	if len(fin.ExecutionResult) == 0 {
		t.Fatal("execution_result must be recorded on the executed request")
	}

	// Payload pinning: the Executor ran the EXACT bytes pinned at request time.
	mu.Lock()
	saw := seenPayload
	mu.Unlock()
	if !jsonEqual(t, saw, pinned) {
		t.Fatalf("Executor saw payload %s, want the pinned %s", saw, pinned)
	}

	// Replay: a further final approval returns the stored result with NO second effect.
	rep, err := svc.Approve(ctxDecide, reqID, newUUIDv4(), "tenant_admin", "late approve", true)
	if err != nil || rep.Status != approvals.StatusExecuted {
		t.Fatalf("replay approve = (status=%q err=%v), want executed/no-error", rep.Status, err)
	}
	if c := counter.Load(); c != 1 {
		t.Fatalf("Executor ran %d times after replay, want still 1 (no second effect)", c)
	}

	// Audit chain (create + winning decision + execute rows) verifies clean for acme.
	ok, brokenSeq, verr := audit.New(store).Verify(ctxReq, "acme")
	if verr != nil || !ok {
		t.Fatalf("audit chain verify: ok=%v brokenSeq=%d err=%v", ok, brokenSeq, verr)
	}

	t.Logf("PASS: fail-closed gate (required=1, no policy row); exactly-once under %d-way race (counter=1); payload pinned; replay no-op", n)
}

// --- HTTP surface harness for the negative-decision table (acceptance #3) ---

type testAuth struct{}

func (testAuth) Authenticate(r *http.Request) (tenant.Principal, error) {
	return tenant.Principal{
		TenantID: r.Header.Get("X-Test-Tenant"),
		UserID:   r.Header.Get("X-Test-User"),
		Scopes:   []string{"role:" + r.Header.Get("X-Test-Role")},
	}, nil
}

// fakeStepUp accepts exactly one code, so the missing/invalid-code paths are exercised deterministically
// (a real TOTP seed is not needed to prove the 401 mapping).
type fakeStepUp struct{}

func (fakeStepUp) VerifyStepUp(_ context.Context, code string) error {
	if code == "111111" {
		return nil
	}
	return errors.New("bad code")
}

type apprClient struct {
	t    *testing.T
	base string
}

func (c *apprClient) approve(id, user, role, code, comment string) int {
	c.t.Helper()
	req, err := http.NewRequest("POST", c.base+"/v1/admin/approvals/"+id+"/approve",
		strings.NewReader(`{"comment":"`+comment+`"}`))
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newUUIDv4())
	req.Header.Set("X-Test-Tenant", "acme")
	req.Header.Set("X-Test-User", user)
	req.Header.Set("X-Test-Role", role)
	if code != "" {
		req.Header.Set("X-MFA-Code", code)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("approve %s: %v", id, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestApprovalsNegativeDecisions is acceptance #3 (table-driven over the HTTP surface): four-eyes,
// approver-role authority, step-up, and expiry each return their exact status code; plus the §9.1
// deadlock guard.
func TestApprovalsNegativeDecisions(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupApprovalSchema(t, admin)

	store := appStore(t, cfg)
	svc := approvals.NewService(approvals.Config{Store: store, Audit: audit.New(store), Now: time.Now})
	noop := func(context.Context, json.RawMessage) error { return nil }
	svc.RegisterExecutor(approvals.ActionKeyBulkDelete, noop)
	svc.RegisterExecutor(approvals.ActionRoutingPublish, noop)

	// An explicit policy that CUSTOMIZES routing_publish to require the operator role — used to prove
	// the approver-role check (a tenant_admin decider fails it) distinctly from coarse RBAC.
	mustExec(t, admin, `insert into approval_policies (tenant_id, action_kind, required_approvals, approver_role, expires_after_s)
		values ('acme','routing_publish',1,'operator',86400)`)

	mux := http.NewServeMux()
	approvals.Routes(mux, approvals.Deps{Service: svc, Auth: testAuth{}, StepUp: fakeStepUp{}})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cl := &apprClient{t: t, base: ts.URL}

	requester := newUUIDv4()
	approver := newUUIDv4()
	ctxReq := ctxFor("acme", requester, "tenant_admin")

	// four-eyes: requester approves own request -> 403.
	_, feID, err := svc.Check(ctxReq, approvals.ActionKeyBulkDelete, json.RawMessage(`{"ids":["x"]}`))
	if err != nil {
		t.Fatalf("check four-eyes req: %v", err)
	}
	if st := cl.approve(feID, requester, "tenant_admin", "111111", "self approve"); st != http.StatusForbidden {
		t.Fatalf("four-eyes: requester approving own = %d, want 403", st)
	}

	// step-up: approver != requester, holds role, but NO X-MFA-Code -> 401.
	_, mfaID, err := svc.Check(ctxReq, approvals.ActionKeyBulkDelete, json.RawMessage(`{"ids":["y"]}`))
	if err != nil {
		t.Fatalf("check mfa req: %v", err)
	}
	if st := cl.approve(mfaID, approver, "tenant_admin", "", "no code"); st != http.StatusUnauthorized {
		t.Fatalf("missing X-MFA-Code = %d, want 401", st)
	}
	if st := cl.approve(mfaID, approver, "tenant_admin", "000000", "wrong code"); st != http.StatusUnauthorized {
		t.Fatalf("invalid X-MFA-Code = %d, want 401", st)
	}

	// approver-role authority: routing_publish needs operator; a tenant_admin decider -> 403.
	_, roleID, err := svc.Check(ctxReq, approvals.ActionRoutingPublish, json.RawMessage(`{"version_id":"v1","payload_hash":"h"}`))
	if err != nil {
		t.Fatalf("check role req: %v", err)
	}
	if st := cl.approve(roleID, approver, "tenant_admin", "111111", "approve"); st != http.StatusForbidden {
		t.Fatalf("approver lacking approver_role = %d, want 403", st)
	}

	// expiry: force a pending request past expires_at, then decide -> 409.
	_, expID, err := svc.Check(ctxReq, approvals.ActionKeyBulkDelete, json.RawMessage(`{"ids":["z"]}`))
	if err != nil {
		t.Fatalf("check expiry req: %v", err)
	}
	mustExec(t, admin, `update approval_requests set expires_at = now() - interval '1 hour' where id = $1`, expID)
	if st := cl.approve(expID, approver, "tenant_admin", "111111", "too late"); st != http.StatusConflict {
		t.Fatalf("decision on expired request = %d, want 409", st)
	}

	// §9.1 deadlock guard: a tenant whose only eligible approver is the requester -> gate refuses.
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('solo','Solo','customer','active')`)
	soloReq := newUUIDv4()
	mustExec(t, admin, `insert into users (id, tenant_id, email, password_hash, role)
		values ($1,'solo','admin@solo.test','x','tenant_admin')`, soloReq)
	guardSvc := approvals.NewService(approvals.Config{
		Store: store, Audit: audit.New(store), Roster: approvals.NewRoster(store), Now: time.Now,
	})
	ctxSolo := ctxFor("solo", soloReq, "tenant_admin")
	if _, _, gerr := guardSvc.Check(ctxSolo, approvals.ActionKeyBulkDelete, json.RawMessage(`{"ids":["a"]}`)); !errors.Is(gerr, approvals.ErrNoEligibleApprover) {
		t.Fatalf("single-approver tenant Check err = %v, want ErrNoEligibleApprover", gerr)
	}

	t.Log("PASS: four-eyes 403, approver-role 403, missing/invalid X-MFA-Code 401, expired 409, single-approver deadlock guard 422")
}

// jsonEqual reports whether two JSON documents are semantically equal (jsonb canonicalizes bytes on
// round-trip, so pinning is asserted by value, not byte-identity).
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var ax, bx any
	if err := json.Unmarshal(a, &ax); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bx); err != nil {
		return false
	}
	aj, _ := json.Marshal(ax)
	bj, _ := json.Marshal(bx)
	return string(aj) == string(bj)
}
