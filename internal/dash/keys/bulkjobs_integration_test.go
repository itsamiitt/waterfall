//go:build integration

// Live-Postgres proofs for durable bulk-job CANCELLATION (OI-API-4 / doc 15 §T3) and AUTO-RESUME
// (OI-KEYS-1c / doc 15 §T5b) of key imports, over migrations 0004/0005/0008/0009/0012 under FORCE
// RLS as the non-superuser dash_app role.
//
//   - TestBulkImportCancelMidRun: a running import observes cancel_requested between waves, stops at
//     a clean terminal 'cancelled', and RETAINS the rows it already committed (idempotent resubmit
//     safe). Cancelling a finished import is a no-op.
//   - TestBulkImportJanitorRequeueAndResume: an executing import whose lease is stolen/expired is
//     RE-QUEUED by the janitor (not failed); the BulkJobRunner reclaims and resumes from the last
//     committed cursor, driving it to 'succeeded' with the correct total and NO double-insert
//     (re-attempted rows are same-batch fingerprint dups — G2 idempotency).
//   - TestBulkImportRunnerRaceNoDoubleClaim (-race): several runners + a janitor contend over
//     re-queued jobs; FOR UPDATE SKIP LOCKED guarantees no double-claim, and every job completes
//     once with the correct total and no double-insert.
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
	"sync"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/dash/queues"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// newBulkHarness wires a keys.Service + its db.Store (needed to build the queues janitor) as the
// non-superuser dash_app role, returning an operator platform context for the import calls.
func newBulkHarness(t *testing.T, cfg pg.Config) (*keys.Service, *db.Store, context.Context) {
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
	backend := secrets.NewPGBackend(store, kr, []byte("test-pepper-bulkjobs"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := keys.NewService(store, backend, audit.New(store), logger)
	operator := tenant.Principal{TenantID: "platform", UserID: newUUID(), Scopes: []string{"role:operator"}}
	return svc, store, tenant.WithPrincipal(context.Background(), operator)
}

// importCSV builds an n-row CSV of unique valid secrets for provider hunter.
func importCSV(n int, marker string) []byte {
	var b strings.Builder
	b.Grow(n * 40)
	b.WriteString("label,secret,region\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "key-%05d,hk_live_%s_%05d_xxxx,us\n", i, marker, i)
	}
	return []byte(b.String())
}

func bjScalar(t *testing.T, admin *pg.Conn, id, col string) string {
	t.Helper()
	return scalar(t, admin, "select coalesce("+col+"::text,'') from bulk_jobs where id = '"+id+"'")
}

func pollBulkStatus(t *testing.T, admin *pg.Conn, id string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bjScalar(t, admin, id, "status") == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("bulk_jobs %s did not reach status %q (got %q, cancel=%q, succeeded=%q)",
		id, want, bjScalar(t, admin, id, "status"), bjScalar(t, admin, id, "cancel_requested"),
		bjScalar(t, admin, id, "succeeded"))
}

func keyCount(t *testing.T, admin *pg.Conn, batchID string) int {
	t.Helper()
	var n int
	fmt.Sscanf(scalar(t, admin, "select count(*) from provider_keys where imported_batch_id = '"+batchID+"'"), "%d", &n)
	return n
}

func TestBulkImportCancelMidRun(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	svc, _, opCtx := newBulkHarness(t, cfg)

	const total = 400
	batchID, err := svc.StartImport(opCtx, "hunter", "csv", importCSV(total, "CANCEL"), "")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	// Request cancellation immediately; the executor polls it on the first wave boundary and stops.
	mustExec(t, admin, "update bulk_jobs set cancel_requested=true where id=$1::uuid", batchID)
	pollBulkStatus(t, admin, batchID, "cancelled", 30*time.Second)

	var succeeded int
	fmt.Sscanf(bjScalar(t, admin, batchID, "succeeded"), "%d", &succeeded)
	if succeeded <= 0 || succeeded >= total {
		t.Fatalf("cancelled mid-run should have 0 < succeeded < %d, got %d", total, succeeded)
	}
	if got := keyCount(t, admin, batchID); got != succeeded {
		t.Fatalf("committed rows must be retained: provider_keys=%d, bulk_jobs.succeeded=%d", got, succeeded)
	}
	if got := scalar(t, admin, "select status from key_import_batches where id = '"+batchID+"'"); got != "cancelled" {
		t.Fatalf("key_import_batches status = %q, want cancelled", got)
	}
	// Idempotent: cancelling the already-terminal job changes nothing.
	mustExec(t, admin, "update bulk_jobs set cancel_requested=true where id=$1::uuid", batchID)
	time.Sleep(200 * time.Millisecond)
	if got := bjScalar(t, admin, batchID, "status"); got != "cancelled" {
		t.Fatalf("re-cancel changed terminal status to %q", got)
	}

	// Cancelling a FINISHED import is a no-op (executor already gone; status stays succeeded).
	fin, err := svc.StartImport(opCtx, "hunter", "csv", importCSV(5, "FINI"), "")
	if err != nil {
		t.Fatalf("StartImport(finished): %v", err)
	}
	pollBulkStatus(t, admin, fin, "succeeded", 30*time.Second)
	mustExec(t, admin, "update bulk_jobs set cancel_requested=true where id=$1::uuid", fin)
	time.Sleep(200 * time.Millisecond)
	if got := bjScalar(t, admin, fin, "status"); got != "succeeded" {
		t.Fatalf("cancel of finished job changed status to %q, want succeeded", got)
	}
	t.Logf("PASS T3 cancel: mid-run import cancelled at succeeded=%d (committed rows retained); re-cancel + cancel-of-finished are no-ops", succeeded)
}

// stealAndRequeue drives an import partway, steals its lease (simulating the executor's death),
// waits for the executor to abort (committed count stabilizes), runs the janitor, and asserts the
// row was RE-QUEUED (not failed). Returns the observed committed-key count at requeue time.
func stealAndRequeue(t *testing.T, admin *pg.Conn, svc *keys.Service, jan *queues.Service, opCtx context.Context, batchID string) int {
	t.Helper()
	// Wait until the initial executor has claimed and is running.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if bjScalar(t, admin, batchID, "status") == "running" && bjScalar(t, admin, batchID, "claimed_by") != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Steal the lease: a different claimant + an already-expired lease. The live executor notices at
	// its next wave renewal (ownership guard) and aborts WITHOUT a terminal write, retaining the
	// staged payload; the janitor then sees an expired-lease running row.
	mustExec(t, admin, "update bulk_jobs set claimed_by='dead-instance', lease_expires_at=now()-interval '2 minutes' where id=$1::uuid", batchID)

	// Wait for the aborting executor to stop committing rows (count stable across two reads).
	prev := -1
	stableDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(stableDeadline) {
		time.Sleep(250 * time.Millisecond)
		cur := keyCount(t, admin, batchID)
		if cur == prev {
			break
		}
		prev = cur
	}
	committed := keyCount(t, admin, batchID)

	n, err := jan.ReclaimExpired(opCtx)
	if err != nil {
		t.Fatalf("janitor ReclaimExpired: %v", err)
	}
	if n < 1 {
		t.Fatalf("janitor reclaimed %d rows, want >=1", n)
	}
	if got := bjScalar(t, admin, batchID, "status"); got != "queued" {
		t.Fatalf("janitor must RE-QUEUE a resumable import (status=%q, want queued)", got)
	}
	return committed
}

func TestBulkImportJanitorRequeueAndResume(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	svc, store, opCtx := newBulkHarness(t, cfg)
	jan := queues.NewService(queues.Config{Store: store})

	const total = 400
	batchID, err := svc.StartImport(opCtx, "hunter", "csv", importCSV(total, "RESUME"), "")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	committed := stealAndRequeue(t, admin, svc, jan, opCtx, batchID)
	if committed <= 0 || committed >= total {
		t.Fatalf("expected a PARTIAL crash (0 < committed < %d), got %d", total, committed)
	}
	t.Logf("crash simulated: %d/%d rows committed, row re-queued by janitor", committed, total)

	// The runner reclaims the re-queued job and resumes from the committed cursor.
	runner := svc.NewBulkJobRunner(0)
	driven, err := runner.RunOnce(opCtx)
	if err != nil {
		t.Fatalf("runner RunOnce: %v", err)
	}
	if driven < 1 {
		t.Fatalf("runner drove %d jobs, want >=1", driven)
	}
	if got := bjScalar(t, admin, batchID, "status"); got != "succeeded" {
		t.Fatalf("after resume, bulk_jobs status = %q, want succeeded", got)
	}
	if got := bjScalar(t, admin, batchID, "succeeded"); got != fmt.Sprint(total) {
		t.Fatalf("after resume, bulk_jobs.succeeded = %q, want %d", got, total)
	}
	// The KEY invariant: no double-insert. Re-attempted rows were recognized as same-batch dups.
	if got := keyCount(t, admin, batchID); got != total {
		t.Fatalf("provider_keys for batch = %d, want %d (double-insert / lost row on resume)", got, total)
	}
	if got := scalar(t, admin, "select status from key_import_batches where id = '"+batchID+"'"); got != "succeeded" {
		t.Fatalf("key_import_batches status = %q, want succeeded", got)
	}
	// No stale duplicate envelopes were left behind by re-sealed same-batch rows.
	if got := scalar(t, admin, "select count(distinct secret_envelope_id) from provider_keys where imported_batch_id = '"+batchID+"'"); got != fmt.Sprint(total) {
		t.Fatalf("distinct envelopes = %q, want %d", got, total)
	}
	_ = store
	t.Logf("PASS T5b resume: janitor re-queued the crashed import; runner resumed cursor->%d; exactly %d keys, no double-insert (G2)", total, total)
}

func TestBulkImportRunnerRaceNoDoubleClaim(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	svc, store, opCtx := newBulkHarness(t, cfg)
	jan := queues.NewService(queues.Config{Store: store})

	// Two independent imports, each crashed mid-run and re-queued (staged rows retained on this
	// instance for both).
	const total = 300
	type job struct{ id string }
	var jobs []job
	for i := 0; i < 2; i++ {
		id, err := svc.StartImport(opCtx, "hunter", "csv", importCSV(total, fmt.Sprintf("RACE%d", i)), "")
		if err != nil {
			t.Fatalf("StartImport %d: %v", i, err)
		}
		stealAndRequeue(t, admin, svc, jan, opCtx, id)
		jobs = append(jobs, job{id: id})
	}

	// Four runners (unique instance ids) + a janitor loop all contend concurrently until both jobs
	// are terminal. SKIP LOCKED must prevent any double-claim; -race must find no data races.
	ctx, cancel := context.WithCancel(opCtx)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		r := svc.NewBulkJobRunner(0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if _, err := r.RunOnce(ctx); err != nil {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			_, _ = jan.ReclaimExpired(ctx)
			time.Sleep(20 * time.Millisecond)
		}
	}()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		done := 0
		for _, j := range jobs {
			if s := bjScalar(t, admin, j.id, "status"); s == "succeeded" || s == "partial" || s == "failed" {
				done++
			}
		}
		if done == len(jobs) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	for _, j := range jobs {
		if got := bjScalar(t, admin, j.id, "status"); got != "succeeded" {
			t.Fatalf("job %s status = %q, want succeeded", j.id, got)
		}
		if got := bjScalar(t, admin, j.id, "succeeded"); got != fmt.Sprint(total) {
			t.Fatalf("job %s succeeded = %q, want %d", j.id, got, total)
		}
		if got := keyCount(t, admin, j.id); got != total {
			t.Fatalf("job %s provider_keys = %d, want %d (double-claim double-inserted)", j.id, got, total)
		}
	}
	_ = store
	t.Logf("PASS T5b -race: %d re-queued imports driven to succeeded by 4 contending runners + janitor; SKIP LOCKED => no double-claim, no double-insert", len(jobs))
}
