package durable_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/durable"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

func ctxFor(tid string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tid})
}

func makeJob(tenantID, subjectID, idemKey string) *job.Job {
	id := job.DeriveID(tenantID, idemKey)
	req := domain.EnrichmentRequest{
		JobID:            id,
		Subject:          domain.Subject{ID: subjectID, Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"}},
		Want:             []domain.Field{domain.FieldWorkEmail},
		ConfidenceTarget: 0.99, // unreachable by one 0.9 provider => exactly one call per job
		CostCeiling:      100,
		ConfigVersion:    "v1",
	}
	return &job.Job{
		ID: id, TenantID: tenantID, IdempotencyKey: idemKey,
		Principal: tenant.Principal{TenantID: tenantID},
		Req:       req, Priority: job.PriorityBulk, Status: job.StatusQueued,
	}
}

func waitAll(t *testing.T, s *durable.Store, ctx context.Context, ids []string, want job.Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		all := true
		for _, id := range ids {
			j, ok, _ := s.Get(ctx, id)
			if !ok || j.Status != want {
				all = false
				break
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for jobs to reach %s", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestStore_CrashRecoversJobsAndOutbox proves the transactional outbox survives a crash:
// jobs submitted then lost to a process exit are recovered as queued + pending.
func TestStore_CrashRecoversJobsAndOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	ctxA := ctxFor("tenant-A")

	s1, err := durable.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, k := range []string{"k0", "k1", "k2"} {
		j := makeJob("tenant-A", "subj-"+k, k)
		if _, err := s1.Submit(ctxA, j); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	s1.Close() // crash before any processing

	// Recover.
	s2, err := durable.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if len(s2.PendingOutbox()) != 3 {
		t.Fatalf("recovery lost outbox intents: pending=%d", len(s2.PendingOutbox()))
	}
	for _, id := range ids {
		j, ok, _ := s2.Get(ctxA, id)
		if !ok || j.Status != job.StatusQueued {
			t.Fatalf("job %s not recovered as queued: %+v", id, j)
		}
	}
	// G1: another tenant cannot see recovered jobs.
	if _, ok, _ := s2.Get(ctxFor("tenant-B"), ids[0]); ok {
		t.Fatal("G1 VIOLATION: cross-tenant read of recovered job")
	}
}

// TestPipeline_CrashRecoveryExactlyOnceCharge is the payoff: jobs durably submitted before
// a crash are re-driven to completion after recovery, and at-least-once redelivery causes
// NO double provider call / double charge (engine G2).
func TestPipeline_CrashRecoveryExactlyOnceCharge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	ctxA := ctxFor("tenant-A")

	// Phase 1: submit durably, then crash before processing.
	s1, _ := durable.OpenStore(path)
	ids := []string{}
	for _, k := range []string{"k0", "k1", "k2"} {
		j := makeJob("tenant-A", "subj-"+k, k)
		if _, err := s1.Submit(ctxA, j); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	s1.Close()

	// Phase 2: recover, wire the engine + relay + workers, and re-drive.
	email := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	engStore := store.NewMemory()
	eng := engine.New(engStore, []provider.Adapter{email}, engine.WithClock(func() time.Time { return time.Unix(1700000000, 0) }))
	planner := router.New(email)
	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		return eng.Run(ctx, req, planner.Plan(req))
	}

	s2, _ := durable.OpenStore(path)
	defer s2.Close()
	q := job.NewQueue(16)
	d := job.NewDispatcher(q, s2, run)
	d.Start(4)
	defer d.Stop()
	relay := durable.NewRelay(s2, q)

	relay.DrainOnce() // publish the 3 recovered jobs
	waitAll(t, s2, ctxA, ids, job.StatusSucceeded)

	if email.Calls() != 3 {
		t.Fatalf("expected exactly 3 provider calls (one per recovered job), got %d", email.Calls())
	}
	if len(s2.PendingOutbox()) != 0 {
		t.Fatalf("outbox should be empty after completion, pending=%d", len(s2.PendingOutbox()))
	}

	// At-least-once redelivery must be safe: force a duplicate delivery of a completed job.
	dup, _, _ := s2.Get(ctxA, ids[0])
	q.Submit(dup)
	time.Sleep(150 * time.Millisecond) // give a worker time to (re)process it

	if email.Calls() != 3 {
		t.Fatalf("redelivery caused a double provider call (G2 broken): calls=%d", email.Calls())
	}
	// A fresh drain publishes nothing (all terminal, none pending).
	if n := relay.DrainOnce(); n != 0 {
		t.Fatalf("drain after completion should publish 0, got %d", n)
	}
}
