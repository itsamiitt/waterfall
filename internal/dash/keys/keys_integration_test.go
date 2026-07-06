//go:build integration

// Package keys_test is the Phase P1 live-Postgres proof for module 3 (Provider Keys + Key Pools +
// bulk import). It is the P1 acceptance gate (doc 12 §P1):
//
//	TestKeysImportSealAndRLS —
//	  1. 1,000-row CSV import -> 202 {job_id}; poll to done; key_import_batches.succeeded=1000;
//	     count(provider_keys WHERE imported_batch_id)=1000; every row has a secret_envelope_id;
//	     ZERO PLAINTEXT across provider_keys columns, audit_log jsonb, and captured slog output.
//	  2. Envelope round-trip via a created key: Seal -> Open returns the exact plaintext.
//	  3. Duplicate import row -> key_import_batches.errors carries "duplicate of ..." via the keyed
//	     fingerprint, without decryption, and the dup is not inserted.
//	  4. RLS: provider_keys / key_budgets / key_import_batches return 0 rows for a customer Tenant;
//	     a BYO key (owner_tenant_id set) is readable only by its owner; secret_envelopes returns 0
//	     rows for every non-platform principal.
//	  5. Zip-bomb fixture -> 422; a "=cmd" cell is stored escaped.
//
// Runs as non-superuser dash_app (superusers bypass RLS). Invoke via scripts/run-rls-test.sh or
// with WATERFALL_PG_DSN set.
package keys_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_app"

// tables this suite rebuilds. The durable bulk-import lifecycle (OI-KEYS-1c) records key_import jobs
// on bulk_jobs (0008) + its cancel_requested column (0012), so the keys suite now spans 0004, 0005,
// 0008, 0009 (cost_rollup_1d, which 0012 alters), and 0012.
var (
	tables0004 = []string{"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
		"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes"}
	tables0005 = []string{"providers", "key_import_batches", "key_pools", "provider_keys",
		"key_pool_members", "key_budgets", "health_schedules", "rotation_triggers"}
	tables0008 = []string{"bulk_jobs", "workers", "queue_defs"}
	tables0009 = []string{"usage_events", "provider_stats_1m", "provider_stats_1h", "provider_stats_1d",
		"key_usage_1m", "key_usage_1h", "key_usage_1d", "tenant_usage_1h", "tenant_usage_1d",
		"cost_rollup_1d", "queue_stats_1m", "queue_stats_1h", "worker_heartbeats", "worker_stats_5m",
		"provider_health_checks", "provider_health_1d"}
	tables0012 = []string{"tenant_invites"}
)

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the keys integration tests")
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

// setupSchema rebuilds migrations 0004 + 0005 and provisions the non-superuser dash_app role.
func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+appRole+" cascade")
	tryExec(admin, "drop role if exists "+appRole)
	tryExec(admin, "drop view if exists providers_catalog cascade")
	allTables := append([]string{}, tables0005...)
	allTables = append(allTables, tables0004...)
	allTables = append(allTables, tables0008...)
	allTables = append(allTables, tables0009...)
	allTables = append(allTables, tables0012...)
	tryExec(admin, "drop table if exists "+strings.Join(allTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")

	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)

	for _, mig := range []string{
		"0004_dash_identity_rbac.sql", "0005_dash_providers_keys.sql",
		"0008_dash_workers_queues.sql", "0009_dash_telemetry.sql", "0012_dash_provisioning_mfa.sql",
	} {
		ddl, err := os.ReadFile("../../../migrations/" + mig)
		if err != nil {
			t.Fatalf("read migration %s: %v", mig, err)
		}
		if err := admin.Exec(string(ddl)); err != nil {
			t.Fatalf("apply migration %s: %v", mig, err)
		}
	}

	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on all tables in schema public to "+appRole)
	mustExec(t, admin, "grant usage, select on all sequences in schema public to "+appRole)

	// Fixtures: platform tenant is seeded by 0004; add a customer tenant + a provider.
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('tenant-a','A','customer','active')`)
	mustExec(t, admin, `insert into providers (id, display_name) values ('hunter','Hunter')`)
}

// stubAuth binds a fixed Principal, standing in for the orchestrator's real authenticator.
type stubAuth struct{ p tenant.Principal }

func (s stubAuth) Authenticate(*http.Request) (tenant.Principal, error) { return s.p, nil }

// syncBuffer is a mutex-guarded buffer so the async import goroutine's slog writes are safe to
// scan after the job completes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestKeysImportSealAndRLS(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	// App wiring as the non-superuser dash_app role (RLS-enforced).
	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
	defer pool.Close()
	store := db.New(pool)

	masterKey := make([]byte, 32)
	_, _ = rand.Read(masterKey)
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(masterKey))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	backend := secrets.NewPGBackend(store, kr, []byte("test-pepper-keys"))
	auditLog := audit.New(store)

	logBuf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, nil))
	svc := keys.NewService(store, backend, auditLog, logger)

	operator := tenant.Principal{TenantID: "platform", UserID: newUUID(), Scopes: []string{"role:operator"}}
	opCtx := tenant.WithPrincipal(context.Background(), operator)

	srv := httptest.NewServer(routesHandler(keys.Deps{
		Store: store, Secrets: backend, Audit: auditLog, Auth: stubAuth{p: operator}, Logger: logger,
	}))
	defer srv.Close()

	// ---- (1) 1,000-row CSV import -> 202 -> poll -> counts + zero plaintext ----
	const n = 1000
	const marker = "pLaInTeXtSeCrEt" // recognizable substring present in every generated secret
	var csv strings.Builder
	csv.WriteString("label,secret,region\n")
	knownSecrets := make([]string, n)
	for i := 0; i < n; i++ {
		secret := fmt.Sprintf("hk_live_%s_%04d", marker, i)
		knownSecrets[i] = secret
		label := fmt.Sprintf("key-%04d", i)
		if i == 0 {
			label = "=cmd_injection" // (5) formula-injection cell, folded into the main import
		}
		fmt.Fprintf(&csv, "%s,%s,us\n", label, secret)
	}

	status, body := postImport(t, srv.URL, "hunter", "csv", []byte(csv.String()), "idem-import-1")
	if status != http.StatusAccepted {
		t.Fatalf("import POST = %d %s, want 202", status, body)
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || accepted.JobID == "" {
		t.Fatalf("import response missing job_id: %s", body)
	}
	batchID := accepted.JobID

	waitBatchDone(t, admin, batchID, 120*time.Second)

	if got := scalar(t, admin, `select status from key_import_batches where id = '`+batchID+`'`); got != "succeeded" {
		t.Fatalf("batch status = %q, want succeeded (errors: %s)", got,
			scalar(t, admin, `select coalesce(errors::text,'') from key_import_batches where id = '`+batchID+`'`))
	}
	if got := scalar(t, admin, `select succeeded from key_import_batches where id = '`+batchID+`'`); got != "1000" {
		t.Fatalf("batch.succeeded = %q, want 1000", got)
	}
	if got := scalar(t, admin, `select count(*) from provider_keys where imported_batch_id = '`+batchID+`'`); got != "1000" {
		t.Fatalf("provider_keys with imported_batch_id = %q, want 1000", got)
	}
	if got := scalar(t, admin, `select count(*) from provider_keys where imported_batch_id = '`+batchID+`' and secret_envelope_id is not null`); got != "1000" {
		t.Fatalf("keys with secret_envelope_id = %q, want 1000", got)
	}

	// (5) the =cmd cell is stored escaped.
	if got := scalar(t, admin, `select label from provider_keys where label like '%cmd_injection'`); got != "'=cmd_injection" {
		t.Fatalf("formula cell stored as %q, want %q", got, "'=cmd_injection")
	}

	// ZERO PLAINTEXT: provider_keys columns, audit_log jsonb, captured slog output.
	pkText := scalar(t, admin, `select coalesce(string_agg(provider_keys::text, ' '), '') from provider_keys`)
	auditText := scalar(t, admin, `select coalesce(string_agg(coalesce(before::text,'')||coalesce(after::text,''), ' '), '') from audit_log`)
	logText := logBuf.String()
	for _, surface := range []struct{ name, text string }{
		{"provider_keys columns", pkText}, {"audit_log jsonb", auditText}, {"slog output", logText},
	} {
		if strings.Contains(surface.text, marker) {
			t.Fatalf("PLAINTEXT LEAK: marker found in %s", surface.name)
		}
		for _, idx := range []int{0, 499, 999} {
			if strings.Contains(surface.text, knownSecrets[idx]) {
				t.Fatalf("PLAINTEXT LEAK: full secret %d found in %s", idx, surface.name)
			}
		}
	}

	// Exercise the HTTP progress endpoint (§4.3 shape) too.
	st, prog := getJSON(t, srv.URL+"/v1/admin/key-imports/"+batchID)
	if st != 200 || prog["status"] != "succeeded" || prog["total"].(float64) != 1000 {
		t.Fatalf("GET key-imports = %d %v", st, prog)
	}
	t.Logf("PASS import gate: 1000 keys sealed, zero plaintext across pk/audit/logs")

	// ---- (2) envelope round-trip via a created key ----
	const rtSecret = "hk_live_roundtrip_ABCDEF012345"
	created, _, err := svc.CreateKey(opCtx, "hunter", keys.CreateKeyInput{Label: "roundtrip", Secret: rtSecret})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	pt, err := backend.Open(opCtx, secrets.EnvelopeID(created.SecretEnvelopeID))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(pt) != rtSecret {
		t.Fatalf("Seal->Open mismatch: got %q", string(pt))
	}
	if created.SecretLast4 != "2345" {
		t.Fatalf("secret_last4 = %q, want 2345", created.SecretLast4)
	}
	t.Logf("PASS envelope round-trip: Seal->Open exact")

	// ---- (3) duplicate import row -> fingerprint match, not inserted, no decryption ----
	dupCSV := "label,secret\ndupA,hk_live_DUPLICATE_ZZZ99\ndupB,hk_live_DUPLICATE_ZZZ99\n"
	st2, dbody := postImport(t, srv.URL, "hunter", "csv", []byte(dupCSV), "idem-import-dup")
	if st2 != http.StatusAccepted {
		t.Fatalf("dup import = %d %s", st2, dbody)
	}
	var dupAccepted struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(dbody, &dupAccepted)
	waitBatchDone(t, admin, dupAccepted.JobID, 30*time.Second)

	dupErrors := scalar(t, admin, `select coalesce(errors::text,'') from key_import_batches where id = '`+dupAccepted.JobID+`'`)
	if !strings.Contains(dupErrors, "duplicate of key") {
		t.Fatalf("dup errors missing 'duplicate of key': %s", dupErrors)
	}
	if got := scalar(t, admin, `select succeeded from key_import_batches where id = '`+dupAccepted.JobID+`'`); got != "1" {
		t.Fatalf("dup batch succeeded = %q, want 1", got)
	}
	if got := scalar(t, admin, `select count(*) from provider_keys where label in ('dupA','dupB')`); got != "1" {
		t.Fatalf("keys inserted for dup material = %q, want 1 (second is a fingerprint dup)", got)
	}
	t.Logf("PASS duplicate detection via keyed fingerprint (no decryption)")

	// ---- (5) zip-bomb import -> 422 ----
	bomb := zipBomb(t)
	stBomb, bombBody := postImport(t, srv.URL, "hunter", "xlsx", bomb, "idem-import-bomb")
	if stBomb != http.StatusUnprocessableEntity {
		t.Fatalf("zip-bomb import = %d %s, want 422", stBomb, bombBody)
	}
	if !bytes.Contains(bombBody, []byte("validation_failed")) {
		t.Fatalf("zip-bomb error code = %s, want validation_failed", bombBody)
	}
	t.Logf("PASS zip-bomb rejected 422; =cmd cell stored escaped")

	// ---- (4) RLS: Class P zero-rows for a customer Tenant; BYO owner-only; envelopes never leak ----
	rlsChecks(t, cfg)
	t.Logf("PASS RLS zero-rows for tenant-a across provider_keys/key_budgets/key_import_batches/secret_envelopes; BYO owner-scoped")
}

// waitBatchDone polls key_import_batches.status until it is terminal or the deadline passes.
func waitBatchDone(t *testing.T, admin *pg.Conn, batchID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := scalar(t, admin, `select status from key_import_batches where id = '`+batchID+`'`)
		if st != "" && st != "running" {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("batch %s did not finish within %s", batchID, timeout)
}

// rlsChecks connects as the non-superuser dash_app role and asserts the Class P isolation
// properties, binding both GUCs via set_config exactly like the dual-GUC tx helper.
func rlsChecks(t *testing.T, cfg pg.Config) {
	t.Helper()
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

	// Seed a platform-managed key, a customer-tenant key, a budget, and a batch as platform.
	set("platform", "operator")
	envPlat := newUUID()
	envBYO := newUUID()
	mustExec(t, raw, `insert into secret_envelopes (id, kind, master_key_id, dek_wrapped, nonce, ciphertext) values ($1,'provider_key','k','\x01','\x01','\x01')`, envPlat)
	mustExec(t, raw, `insert into secret_envelopes (id, kind, master_key_id, dek_wrapped, nonce, ciphertext) values ($1,'provider_key','k','\x01','\x01','\x01')`, envBYO)
	platKey := newUUID()
	byoKey := newUUID()
	mustExec(t, raw, `insert into provider_keys (id, provider_id, secret_envelope_id) values ($1,'hunter',$2::uuid)`, platKey, envPlat)
	mustExec(t, raw, `insert into provider_keys (id, provider_id, secret_envelope_id, owner_tenant_id) values ($1,'hunter',$2::uuid,'tenant-a')`, byoKey, envBYO)
	mustExec(t, raw, `insert into key_budgets (key_id, day, month) values ($1, current_date, to_char(now(),'YYYY-MM'))`, byoKey)
	batch := newUUID()
	mustExec(t, raw, `insert into key_import_batches (id, provider_id, source) values ($1,'hunter','csv')`, batch)

	// A customer Tenant sees zero Class P rows, EXCEPT its own BYO key via the owner projection.
	set("tenant-a", "tenant_admin")
	assertZero(t, raw, "key_budgets", `select count(*) from key_budgets`)
	assertZero(t, raw, "key_import_batches", `select count(*) from key_import_batches`)
	assertZero(t, raw, "secret_envelopes", `select count(*) from secret_envelopes`)
	if got := scalar(t, raw, `select count(*) from provider_keys where id = '`+platKey+`'`); got != "0" {
		t.Fatalf("tenant-a saw platform-managed key (%s rows), want 0", got)
	}
	if got := scalar(t, raw, `select count(*) from provider_keys where id = '`+byoKey+`'`); got != "1" {
		t.Fatalf("tenant-a could not read its own BYO key (%s rows), want 1", got)
	}

	// A different customer Tenant cannot read tenant-a's BYO key.
	set("tenant-b", "tenant_admin")
	if got := scalar(t, raw, `select count(*) from provider_keys where id = '`+byoKey+`'`); got != "0" {
		t.Fatalf("tenant-b read tenant-a's BYO key (%s rows), want 0", got)
	}

	// An operator on a customer-tenant binding still sees zero secret_envelopes (never operator-readable).
	set("tenant-b", "operator")
	assertZero(t, raw, "secret_envelopes (operator)", `select count(*) from secret_envelopes`)
}

func assertZero(t *testing.T, c *pg.Conn, name, sql string) {
	t.Helper()
	if got := scalar(t, c, sql); got != "0" {
		t.Fatalf("%s: cross-tenant SELECT returned %s rows, want 0", name, got)
	}
}

// --- HTTP + fixture helpers ---

// routesHandler mounts keys.Routes on a fresh mux and returns it as an http.Handler.
func routesHandler(d keys.Deps) http.Handler {
	mux := http.NewServeMux()
	keys.Routes(mux, d)
	return mux
}

// postImport uploads a file via multipart with the given format, an Idempotency-Key, and returns
// the response status + body.
func postImport(t *testing.T, baseURL, providerID, format string, file []byte, idemKey string) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("format", format)
	fw, err := mw.CreateFormFile("file", "import."+format)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(file); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_ = mw.Close()

	req, _ := http.NewRequest("POST", baseURL+"/v1/admin/providers/"+providerID+"/keys/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST import: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

// zipBomb builds a small .xlsx-shaped archive whose declared uncompressed:compressed ratio trips
// the reader's decompression-ratio guard. ~4 MiB of one byte deflates to a few KB.
func zipBomb(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("A"), 4<<20)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}
