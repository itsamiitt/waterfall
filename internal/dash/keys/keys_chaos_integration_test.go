//go:build integration

// Chaos + load proofs for the bulk-import pipeline (doc 13 §7 "Poison import row" drill + §6 L3
// "Import 50k rows"; closes OI-P12-1 chaos + the L3 single-instance measurement for OI-TS-3):
//
//   - TestPoisonImportRowIsolation: a bulk import where a poison row (malformed: empty required
//     secret) and a duplicate row sit among many good rows. The per-row pipeline must ISOLATE the
//     poison — record it in key_import_batches.errors, import every good row, and reach the terminal
//     'partial' state cleanly (no crash, no all-or-nothing loss).
//   - TestImportLoad50k: a 50,000-row import (the doc 04 §4 row cap) run for real on this box, timed
//     end to end. It records the MEASURED single-instance number written back into doc 13 §6 L3.
//     This is a dev single-instance measurement, NOT the staging target.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package keys_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// newImportHarness wires a keys.Service over a fresh pool as the non-superuser dash_app role, with
// a discarding logger (these tests assert DB state, not log contents). Returns the service + an
// operator platform context for the import calls.
func newImportHarness(t *testing.T, cfg pg.Config) (*keys.Service, context.Context) {
	t.Helper()
	appCfg := cfg
	appCfg.User = appRole
	pool := pg.NewPool(appCfg, 8)
	t.Cleanup(pool.Close)
	store := db.New(pool)

	masterKey := make([]byte, 32)
	_, _ = rand.Read(masterKey)
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(masterKey))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	backend := secrets.NewPGBackend(store, kr, []byte("test-pepper-chaos"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := keys.NewService(store, backend, audit.New(store), logger)

	operator := tenant.Principal{TenantID: "platform", UserID: newUUID(), Scopes: []string{"role:operator"}}
	return svc, tenant.WithPrincipal(context.Background(), operator)
}

// TestPoisonImportRowIsolation proves one malformed + one duplicate row are isolated while every
// good row imports and the batch lands in 'partial'.
func TestPoisonImportRowIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	svc, opCtx := newImportHarness(t, cfg)

	const good = 500
	const dupMarker = "hk_live_POISON_dup_0001"
	var b strings.Builder
	b.WriteString("label,secret,region\n")
	// Row 1 is the original the later duplicate collides with.
	fmt.Fprintf(&b, "orig-0001,%s,us\n", dupMarker)
	for i := 2; i <= good; i++ {
		fmt.Fprintf(&b, "key-%04d,hk_live_POISON_%04d,us\n", i, i)
	}
	// Poison row: malformed — the required secret column is empty.
	b.WriteString("malformed-row,,us\n")
	// Duplicate row: same material as row 1 -> caught by keyed fingerprint, not inserted.
	fmt.Fprintf(&b, "dup-collision,%s,us\n", dupMarker)

	batchID, err := svc.StartImport(opCtx, "hunter", "csv", []byte(b.String()), "")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	waitBatchDone(t, admin, batchID, 120*time.Second)

	q := func(sql string) string {
		return scalar(t, admin, sql+" from key_import_batches where id = '"+batchID+"'")
	}
	if got := q("select status"); got != "partial" {
		t.Fatalf("batch status = %q, want partial (errors: %s)", got, q("select coalesce(errors::text,'')"))
	}
	if got := q("select succeeded"); got != fmt.Sprint(good) {
		t.Fatalf("succeeded = %q, want %d", got, good)
	}
	if got := q("select failed"); got != "2" {
		t.Fatalf("failed = %q, want 2 (one malformed + one duplicate)", got)
	}
	if got := scalar(t, admin, "select count(*) from provider_keys where imported_batch_id = '"+batchID+"'"); got != fmt.Sprint(good) {
		t.Fatalf("provider_keys imported = %q, want %d (good rows must import despite the poison)", got, good)
	}
	errs := q("select coalesce(errors::text,'')")
	for _, want := range []string{"validation_failed", "conflict", "error_summary"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("errors json missing %q: %s", want, errs)
		}
	}
	// Isolation also means zero plaintext leaked into the errors payload.
	if strings.Contains(errs, dupMarker) || strings.Contains(errs, "POISON_") {
		t.Fatalf("PLAINTEXT LEAK: import errors payload contains key material: %s", errs)
	}
	t.Logf("PASS poison-row: 2 rows isolated (malformed + duplicate) in key_import_batches.errors; %d good rows imported; batch terminal=partial (no crash)", good)
}

// TestImportLoad50k runs the doc 04 §4 row-cap import for real and records the measured timing.
// It is an ON-DEMAND load fixture (docs/13 §1 "Load: on demand", §6 L3): the per-row
// seal→dup-check→insert path is 3 serial transactions/row, so a full 50k run is ~15 min of sustained
// write load — too heavy, and under concurrent full-suite load too destabilizing for the shared
// ephemeral cluster, for the routine RLS gate. It SKIPS under -short (which scripts/run-rls-test.sh
// passes); the P12 measurement in docs/13 §6 was captured by running it on-demand WITHOUT -short.
func TestImportLoad50k(t *testing.T) {
	if testing.Short() {
		t.Skip("on-demand load fixture (~15m single instance); runs without -short (docs/13 §6 L3)")
	}
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	svc, opCtx := newImportHarness(t, cfg)

	const n = 50000
	var b strings.Builder
	b.Grow(n * 40)
	b.WriteString("label,secret,region\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "key-%05d,hk_live_LOAD50K_%05d_xxxxxx,us\n", i, i)
	}

	start := time.Now()
	batchID, err := svc.StartImport(opCtx, "hunter", "csv", []byte(b.String()), "")
	if err != nil {
		t.Fatalf("StartImport(50k): %v", err)
	}
	waitBatchDone(t, admin, batchID, 25*time.Minute)
	elapsed := time.Since(start)

	if got := scalar(t, admin, "select status from key_import_batches where id = '"+batchID+"'"); got != "succeeded" {
		t.Fatalf("batch status = %q, want succeeded (errors: %s)", got,
			scalar(t, admin, "select coalesce(errors::text,'') from key_import_batches where id = '"+batchID+"'"))
	}
	if got := scalar(t, admin, "select succeeded from key_import_batches where id = '"+batchID+"'"); got != fmt.Sprint(n) {
		t.Fatalf("succeeded = %q, want %d", got, n)
	}
	if got := scalar(t, admin, "select count(*) from provider_keys where imported_batch_id = '"+batchID+"' and secret_envelope_id is not null"); got != fmt.Sprint(n) {
		t.Fatalf("sealed keys = %q, want %d", got, n)
	}
	rate := float64(n) / elapsed.Seconds()
	t.Logf("MEASURED 50k import (dev, single instance): %d rows sealed + inserted in %s (%.0f rows/s); zero plaintext contract enforced by the P1 gate (TestKeysImportSealAndRLS)",
		n, elapsed.Round(time.Millisecond), rate)
}
