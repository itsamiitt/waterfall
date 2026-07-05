//go:build integration

// Package rotation's live-Postgres proofs (P2 acceptance #1 and #5, doc 12):
//
//	TestRotationLeaseNoOverLease — a 50-goroutine lease storm through the bucket registry against a
//	  real key_budgets row with daily_limit N: total granted leases <= N, asserted from key_budgets
//	  (no over-lease under concurrency; the guarded UPDATE is the serialization point).
//	TestRotationEngineE2E — an Enrichment-Job-style path runs through provider.Call with a leased
//	  key; Done(outcome) attributes the call to the key_id; rotating the key mid-run (create
//	  successor -> add to pool -> shift status via the state machine -> archive old) completes with
//	  zero AUTH failures.
//
// Runs as non-superuser dash_app (superusers bypass RLS). Invoke via scripts/run-rls-test.sh or
// with WATERFALL_PG_DSN set.
package rotation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	dkeys "github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_app"

var (
	tables0004 = []string{"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
		"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes"}
	tables0005 = []string{"providers", "key_import_batches", "key_pools", "provider_keys",
		"key_pool_members", "key_budgets", "health_schedules", "rotation_triggers"}
	grantTables = []string{"tenants", "users", "audit_log", "audit_chain_heads", "api_access_log",
		"secret_envelopes", "providers", "key_import_batches", "key_pools", "provider_keys",
		"key_pool_members", "key_budgets", "rotation_triggers"}
)

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the rotation integration tests")
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

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// setupSchema rebuilds migrations 0004 + 0005 and provisions the non-superuser dash_app role.
func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+appRole+" cascade")
	tryExec(admin, "drop role if exists "+appRole)
	tryExec(admin, "drop view if exists providers_catalog cascade")
	tryExec(admin, "drop table if exists "+strings.Join(append(append([]string{}, tables0005...), tables0004...), ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")

	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	for _, mig := range []string{"0004_dash_identity_rbac.sql", "0005_dash_providers_keys.sql"} {
		ddl, err := os.ReadFile("../../../migrations/" + mig)
		if err != nil {
			t.Fatalf("read migration %s: %v", mig, err)
		}
		if err := admin.Exec(string(ddl)); err != nil {
			t.Fatalf("apply migration %s: %v", mig, err)
		}
	}

	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on "+strings.Join(grantTables, ", ")+" to "+appRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+appRole)

	mustExec(t, admin, `insert into providers (id, display_name, op_state, status) values ('hunter','Hunter','enabled','ACTIVE-CANDIDATE')`)
}

// appStore wires a db.Store connected as the non-superuser dash_app role (RLS-enforced).
func appStore(t *testing.T, cfg pg.Config) *db.Store {
	t.Helper()
	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 12)
	t.Cleanup(pool.Close)
	return db.New(pool)
}

func newBackend(t *testing.T, store *db.Store) secrets.Backend {
	t.Helper()
	mk := make([]byte, 32)
	_, _ = rand.Read(mk)
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(mk))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return secrets.NewPGBackend(store, kr, []byte("test-pepper-rotation"))
}

func operatorCtx() context.Context {
	op := tenant.Principal{TenantID: "platform", UserID: newUUID(), Scopes: []string{"role:operator"}}
	return tenant.WithPrincipal(context.Background(), op)
}

// TestRotationLeaseNoOverLease is P2 acceptance #1 against live key_budgets.
func TestRotationLeaseNoOverLease(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	// Seed one Provider Key (with a sealed-envelope FK) whose budget will be leased against.
	env := newUUID()
	mustExec(t, admin, `insert into secret_envelopes (id, kind, master_key_id, dek_wrapped, nonce, ciphertext) values ($1,'provider_key','k','\x01','\x01','\x01')`, env)
	keyID := newUUID()
	mustExec(t, admin, `insert into provider_keys (id, provider_id, secret_envelope_id, daily_limit) values ($1,'hunter',$2::uuid,250)`, keyID, env)

	store := appStore(t, cfg)
	reg := newBucketRegistry(newPGStore(store))

	const (
		limit        = int64(250)
		goroutines   = 50
		perGoroutine = 40 // 2000 attempts >> 250
	)
	var granted atomic.Int64
	var wg sync.WaitGroup
	ctx := operatorCtx()
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if err := reg.draw(ctx, keyID, limit); err == nil {
					granted.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	dayLeased := scalar(t, admin, `select day_leased from key_budgets where key_id = '`+keyID+`'`)
	if dayLeased == "" {
		t.Fatal("no key_budgets row was created for the leased key")
	}
	var leasedN int64
	fmt.Sscan(dayLeased, &leasedN)
	if leasedN > limit {
		t.Fatalf("OVER-LEASE: key_budgets.day_leased = %d, daily_limit is %d", leasedN, limit)
	}
	if granted.Load() > limit {
		t.Fatalf("OVER-LEASE: granted %d leases, daily_limit is %d", granted.Load(), limit)
	}
	if granted.Load() != limit {
		t.Fatalf("granted %d leases with demand %d >> limit %d; want exactly %d",
			granted.Load(), goroutines*perGoroutine, limit, limit)
	}
	t.Logf("PASS engine no over-lease @50 goroutines: granted=%d, key_budgets.day_leased=%d, daily_limit=%d",
		granted.Load(), leasedN, limit)
}

// TestRotationEngineE2E is P2 acceptance #5.
func TestRotationEngineE2E(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	store := appStore(t, cfg)
	backend := newBackend(t, store)
	auditLog := audit.New(store)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	keySvc := dkeys.NewService(store, backend, auditLog, logger)

	ctx := operatorCtx()

	// A fake Provider that REQUIRES an injected key header (proving the lease secret reached the
	// wire) and always returns 200 — so a correct run has zero AUTH failures.
	var httpCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		if r.Header.Get("X-API-Key") == "" {
			w.WriteHeader(http.StatusUnauthorized) // would drive AUTH -> auth_failed
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Pool + first key.
	pool, err := keySvc.CreatePool(ctx, "hunter", "default", "round_robin", "", "")
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	key1, _, err := keySvc.CreateKey(ctx, "hunter", dkeys.CreateKeyInput{
		Label: "k1", Secret: "hk_live_KEY_ONE_SECRET", PoolIDs: []string{pool.ID},
	})
	if err != nil {
		t.Fatalf("CreateKey key1: %v", err)
	}
	selector := pool.Selector() // "hunter:default"

	// The rotation engine as the egress LeaseResolver.
	eng := New(Config{
		Store: NewStore(store), Audit: auditLog, Secrets: NewSecretOpener(backend),
		Bandit: bandit.New(), Now: time.Now, Logger: logger,
	})

	adapter := newProbeAdapter(srv.URL, eng, selector)

	// --- batch 1: 5 calls, all attributed to key1 ---
	runCalls(t, ctx, adapter, 5)
	snap1, err := eng.SelectionState(ctx, pool.ID)
	if err != nil {
		t.Fatalf("SelectionState 1: %v", err)
	}
	if got := leasesFor(snap1, key1.ID); got != 5 {
		t.Fatalf("batch 1 attribution: key1 leases=%d, want 5 (%+v)", got, snap1.Keys)
	}
	if oks := oksFor(snap1, key1.ID); oks != 5 {
		t.Fatalf("batch 1: key1 OKs=%d, want 5 (zero-AUTH run)", oks)
	}
	t.Logf("PASS attribution: batch 1 fully attributed to key1 (%s), 5 OK", key1.ID)

	// --- rotate mid-run: create successor -> test -> shift state -> archive old ---
	rot, err := keySvc.RotateKey(ctx, key1.ID, "hk_live_KEY_TWO_SECRET", 300)
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	// "test" the successor with a live bounded provider.Call (a real probe through the engine).
	runCalls(t, ctx, newProbeAdapter(srv.URL, eng, selector), 1)
	// Add the successor to the pool and shift the old key rotating -> archived via the state machine.
	if _, err := keySvc.PutMembers(ctx, pool.ID, []string{key1.ID, rot.SuccessorKeyID}); err != nil {
		t.Fatalf("PutMembers: %v", err)
	}
	if err := eng.trigger.Apply(ctx, key1.ID, StateRotating, StateArchived, "", false); err != nil {
		t.Fatalf("state-machine rotating->archived: %v", err)
	}
	eng.Invalidate(selector) // reload the pool: key1 archived (skip), key2 active

	// --- batch 2: 5 calls, now attributed to the successor key2 ---
	runCalls(t, ctx, newProbeAdapter(srv.URL, eng, selector), 5)
	snap2, err := eng.SelectionState(ctx, pool.ID)
	if err != nil {
		t.Fatalf("SelectionState 2: %v", err)
	}
	if got := leasesFor(snap2, rot.SuccessorKeyID); got != 5 {
		t.Fatalf("batch 2 attribution: key2 leases=%d, want 5 (%+v)", got, snap2.Keys)
	}
	if avail := availFor(snap2, key1.ID); avail {
		t.Fatalf("archived key1 is still available after rotation")
	}

	// Zero AUTH failures: no key ended auth_failed/disabled; DB agrees.
	k1status := scalar(t, admin, `select status from provider_keys where id = '`+key1.ID+`'`)
	k2status := scalar(t, admin, `select status from provider_keys where id = '`+rot.SuccessorKeyID+`'`)
	if k1status != "archived" {
		t.Fatalf("key1 final status = %q, want archived", k1status)
	}
	if k2status != "active" {
		t.Fatalf("key2 final status = %q, want active", k2status)
	}
	if n := scalar(t, admin, `select count(*) from provider_keys where status in ('auth_failed','disabled')`); n != "0" {
		t.Fatalf("run produced %s auth_failed/disabled keys, want 0 AUTH failures", n)
	}
	t.Logf("PASS engine E2E: rotation mid-run shifted attribution key1->key2, zero AUTH failures (k1=%s k2=%s, %d HTTP calls)",
		k1status, k2status, httpCalls.Load())
}

// newProbeAdapter builds an HTTPAdapter whose Transport is the AuthInjector over the rotation
// engine (LeaseResolver), so every call draws a lease, injects the secret, and reports its Outcome.
func newProbeAdapter(baseURL string, eng *Engine, selector string) *provider.HTTPAdapter {
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: provider.NewAuthInjector(nil, eng),
	}
	return &provider.HTTPAdapter{
		NameV:   "hunter",
		BaseURL: baseURL,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyHeader,
			HeaderName:      "X-API-Key",
			KeyPoolSelector: selector,
		},
		Decode: func([]byte) (provider.Result, error) { return provider.Result{}, nil },
	}
}

func runCalls(t *testing.T, ctx context.Context, adapter *provider.HTTPAdapter, n int) {
	t.Helper()
	br := provider.NewBreaker(10, time.Second, nil)
	pol := provider.CallPolicy{Timeout: 3 * time.Second, MaxAttempts: 1}
	for i := 0; i < n; i++ {
		if _, err := provider.Call(ctx, adapter, provider.Request{}, pol, br, nil); err != nil {
			t.Fatalf("provider.Call %d: %v", i, err)
		}
	}
}

func leasesFor(s Snapshot, keyID string) int64 {
	for _, k := range s.Keys {
		if k.KeyID == keyID {
			return k.Leases
		}
	}
	return -1
}

func oksFor(s Snapshot, keyID string) int64 {
	for _, k := range s.Keys {
		if k.KeyID == keyID {
			return k.OKs
		}
	}
	return -1
}

func availFor(s Snapshot, keyID string) bool {
	for _, k := range s.Keys {
		if k.KeyID == keyID {
			return k.Available
		}
	}
	return false
}
