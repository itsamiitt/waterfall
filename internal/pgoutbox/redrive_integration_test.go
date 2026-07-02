//go:build integration

// Live test for the DLQ redrive/replay path (docs/40): a parked poison job is reset by Redrive
// (tenant-scoped) and then re-delivered and completed by a now-working worker. Set
// WATERFALL_PG_DSN.
package pgoutbox_test

import (
	"context"
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

func TestPGOutbox_RedriveReplaysParkedJob(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setup(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls"
	outbox, err := pgoutbox.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()
	engineStore, err := pgstore.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open pgstore: %v", err)
	}
	defer engineStore.Close()

	ctxA := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A"})
	ctxB := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-B"})

	job1 := &job.Job{
		ID:        "redrive-1",
		TenantID:  "tenant-A",
		Principal: tenant.Principal{TenantID: "tenant-A"},
		Status:    job.StatusQueued,
		Req: domain.EnrichmentRequest{
			JobID:            "redrive-1",
			Subject:          domain.Subject{ID: "subj-r1", Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"}},
			Want:             []domain.Field{domain.FieldWorkEmail},
			ConfidenceTarget: 0.8,
			CostCeiling:      100,
		},
	}
	if ok, err := outbox.Submit(ctxA, job1); err != nil || !ok {
		t.Fatalf("submit: ok=%v err=%v", ok, err)
	}

	relayConn, err := pg.Connect(cfg) // privileged claim connection
	if err != nil {
		t.Fatalf("connect relay: %v", err)
	}
	defer relayConn.Close()

	// --- Phase 1: park it. No worker drains the throwaway queue, so it never acks and, after
	// maxAttempts deliveries, is dead-lettered.
	const maxAtt = 2
	parkQueue := job.NewQueue(64)
	parkRelay := pgoutbox.NewRelay(relayConn, parkQueue, time.Millisecond, pgoutbox.WithMaxAttempts(maxAtt))
	for i := 0; i < maxAtt+2; i++ {
		if _, err := parkRelay.DrainOnce(); err != nil {
			t.Fatalf("park drain: %v", err)
		}
		time.Sleep(4 * time.Millisecond)
	}
	if dls, err := outbox.DeadLetters(ctxA, 10); err != nil || len(dls) != 1 || dls[0].JobID != "redrive-1" {
		t.Fatalf("job should be dead-lettered before redrive, got %+v (err=%v)", dls, err)
	}

	// G1: another tenant cannot redrive tenant-A's job.
	if ok, err := outbox.Redrive(ctxB, "redrive-1"); err != nil || ok {
		t.Fatalf("tenant-B must not redrive tenant-A's job: ok=%v err=%v", ok, err)
	}

	// --- Redrive as the owning tenant.
	if ok, err := outbox.Redrive(ctxA, "redrive-1"); err != nil || !ok {
		t.Fatalf("redrive should reset the parked job: ok=%v err=%v", ok, err)
	}
	// It is no longer dead-lettered.
	if dls, err := outbox.DeadLetters(ctxA, 10); err != nil || len(dls) != 0 {
		t.Fatalf("job should leave the DLQ after redrive, got %+v (err=%v)", dls, err)
	}

	// --- Phase 2: a now-working worker re-delivers and completes it.
	now := time.Unix(1_700_000_000, 0)
	fake := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := engine.New(engineStore, []provider.Adapter{fake}, engine.WithClock(func() time.Time { return now }))
	planner := router.New(fake)
	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		return eng.Run(ctx, req, planner.Plan(req))
	}
	workQueue := job.NewQueue(64)
	dispatcher := job.NewDispatcher(workQueue, outbox, run)
	dispatcher.Start(4)
	defer dispatcher.Stop()
	// Realistic visibility (> worker time): deliver once, then let the worker finish. A tiny
	// visibility here would re-claim and re-dead-letter the in-flight job (the slow-job hazard
	// documented in docs/39 §4), so we deliver exactly once and poll for the terminal state.
	workRelay := pgoutbox.NewRelay(relayConn, workQueue, 2*time.Second, pgoutbox.WithMaxAttempts(maxAtt))
	if _, err := workRelay.DrainOnce(); err != nil {
		t.Fatalf("work drain: %v", err)
	}
	var final *job.Job
	for i := 0; i < 100; i++ {
		j, ok, err := outbox.Get(ctxA, "redrive-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if ok && j.Status == job.StatusSucceeded {
			final = j
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final == nil {
		t.Fatal("redriven job did not complete")
	}

	// It completed and left the DLQ for good; a second redrive is a no-op (not dead anymore).
	if we, ok := final.Outcome.Filled[domain.FieldWorkEmail]; !ok || we.Value != "jane@acme.com" {
		t.Fatalf("completed job should have filled work_email, got %+v", final.Outcome.Filled)
	}
	if ok, err := outbox.Redrive(ctxA, "redrive-1"); err != nil || ok {
		t.Fatalf("redrive of a non-dead job must be a no-op: ok=%v err=%v", ok, err)
	}
}
