//go:build integration

// Live tests for the Postgres transactional-outbox durable job queue: normal delivery,
// crash + at-least-once redelivery (with G2 making it exactly-once-effective), the
// visibility-timeout recovery of an in-flight claim, and tenant isolation. Set
// WATERFALL_PG_DSN.
package pgoutbox_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgoutbox"
	"github.com/enrichment/waterfall/internal/pgstore"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/tenant"
)

func dsn(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run pgoutbox integration tests")
	}
	return pg.ParseDSN(d)
}

func setup(t *testing.T, admin *pg.Conn) {
	t.Helper()
	for _, s := range []string{
		"drop table if exists job_outbox cascade",
		"drop owned by app_rls cascade",
		"drop role if exists app_rls",
		"drop table if exists field_versions, idempotency_ledger, cost_ledger cascade",
		"drop function if exists app_current_tenant() cascade",
	} {
		_ = admin.Exec(s)
	}
	for _, f := range []string{"../../migrations/0001_init.sql", "../../migrations/0002_job_outbox.sql", "../../migrations/0003_outbox_dlq.sql"} {
		ddl, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := admin.Exec(string(ddl)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := admin.Exec("grant select, insert, update on field_versions, idempotency_ledger, cost_ledger, job_outbox to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func TestPGOutbox_DurableDeliveryAndCrashSafety(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setup(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls"
	outbox, err := pgoutbox.Open(appCfg, 4) // job store/submitter (tenant-scoped)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()
	engineStore, err := pgstore.Open(appCfg, 4) // engine data + G2/G4 ledgers
	if err != nil {
		t.Fatalf("open pgstore: %v", err)
	}
	defer engineStore.Close()

	now := time.Unix(1_700_000_000, 0)
	fake := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := engine.New(engineStore, []provider.Adapter{fake}, engine.WithClock(func() time.Time { return now }))
	planner := router.New(fake)
	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		return eng.Run(ctx, req, planner.Plan(req))
	}

	queue := job.NewQueue(64)
	dispatcher := job.NewDispatcher(queue, outbox, run)
	dispatcher.Start(4)
	defer dispatcher.Stop()

	// Relay on a privileged (superuser) connection — sees all tenants' rows.
	relayConn, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect relay: %v", err)
	}
	defer relayConn.Close()
	relay := pgoutbox.NewRelay(relayConn, queue, 1*time.Second)

	ctxA := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A"})
	ctxB := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-B"})
	mkJob := func(id, subj string) *job.Job {
		return &job.Job{
			ID:        id,
			TenantID:  "tenant-A",
			Principal: tenant.Principal{TenantID: "tenant-A"},
			Status:    job.StatusQueued,
			Req: domain.EnrichmentRequest{
				JobID:            id,
				Subject:          domain.Subject{ID: subj, Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"}},
				Want:             []domain.Field{domain.FieldWorkEmail},
				ConfidenceTarget: 0.8,
				CostCeiling:      20,
				ConfigVersion:    "v1",
			},
		}
	}

	// ===== (1) normal delivery =====
	if ok, err := outbox.Submit(ctxA, mkJob("job-1", "subj-1")); err != nil || !ok {
		t.Fatalf("submit job-1: ok=%v err=%v", ok, err)
	}
	if n, err := relay.DrainOnce(); err != nil || n != 1 {
		t.Fatalf("drain job-1: n=%d err=%v", n, err)
	}
	j1 := waitTerminal(t, outbox, ctxA, "job-1")
	if fake.Calls() != 1 {
		t.Fatalf("job-1 should call the provider once, got %d", fake.Calls())
	}
	if j1.Outcome == nil || j1.Outcome.Filled[domain.FieldWorkEmail].Value != "jane@acme.com" {
		t.Fatalf("job payload/outcome did not round-trip through JSONB: %+v", j1.Outcome)
	}
	// pending cleared -> a second drain enqueues nothing.
	if n, _ := relay.DrainOnce(); n != 0 {
		t.Fatalf("completed job should not be re-claimed, got n=%d", n)
	}

	// ===== (2) crash + at-least-once redelivery (G2 makes it exactly-once-effective) =====
	// Simulate a crash where the durable terminal ack was lost: the row is pending again.
	if err := admin.Exec("update job_outbox set pending = true, claimed_at = null where job_id = 'job-1'"); err != nil {
		t.Fatalf("simulate crash: %v", err)
	}
	callsBefore := fake.Calls()
	if n, err := relay.DrainOnce(); err != nil || n != 1 {
		t.Fatalf("redelivery drain: n=%d err=%v", n, err)
	}
	waitTerminal(t, outbox, ctxA, "job-1")
	if fake.Calls() != callsBefore {
		t.Fatalf("G2 breach: redelivery triggered %d new provider call(s)", fake.Calls()-callsBefore)
	}

	// ===== (3) visibility timeout: a freshly-claimed in-flight row is not re-claimed; a
	// stale one is (crash recovery) =====
	if ok, err := outbox.Submit(ctxA, mkJob("job-2", "subj-2")); err != nil || !ok {
		t.Fatalf("submit job-2: ok=%v err=%v", ok, err)
	}
	// Simulate "another relay just claimed it" (recent claimed_at, still pending).
	if err := admin.Exec("update job_outbox set claimed_at = now() where job_id = 'job-2'"); err != nil {
		t.Fatalf("mark in-flight: %v", err)
	}
	if n, err := relay.DrainOnce(); err != nil || n != 0 {
		t.Fatalf("recently-claimed row must be skipped within visibility: n=%d err=%v", n, err)
	}
	// Now make the claim stale (older than the 1s visibility) -> recoverable.
	if err := admin.Exec("update job_outbox set claimed_at = now() - interval '5 seconds' where job_id = 'job-2'"); err != nil {
		t.Fatalf("age the claim: %v", err)
	}
	if n, err := relay.DrainOnce(); err != nil || n != 1 {
		t.Fatalf("stale claim must be recoverable: n=%d err=%v", n, err)
	}
	waitTerminal(t, outbox, ctxA, "job-2")

	// ===== (4) tenant isolation + mismatch =====
	if _, ok, _ := outbox.Get(ctxB, "job-1"); ok {
		t.Fatal("tenant-B must not see tenant-A's job (RLS)")
	}
	bad := mkJob("job-3", "subj-3")
	bad.TenantID = "tenant-B" // principal is tenant-A -> mismatch
	if _, err := outbox.Submit(ctxA, bad); !errors.Is(err, pgoutbox.ErrTenantMismatch) {
		t.Fatalf("submit with mismatched tenant should error, got %v", err)
	}
}

func waitTerminal(t *testing.T, s *pgoutbox.Store, ctx context.Context, id string) *job.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if ok && (j.Status == job.StatusSucceeded || j.Status == job.StatusFailed) {
			if j.Status == job.StatusFailed {
				t.Fatalf("job %s failed: %s", id, j.Err)
			}
			return j
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state", id)
	return nil
}
