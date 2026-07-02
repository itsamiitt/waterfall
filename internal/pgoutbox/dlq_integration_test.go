//go:build integration

// Live test for the outbox dead-letter path (docs/39): a poison job that never reaches a
// terminal state is redelivered up to max attempts, then PARKED (dead=true, pending=false) —
// it stops being claimed, the dead-letter hook fires once, and it surfaces in the tenant-scoped
// DeadLetters read (and only to its own tenant). Set WATERFALL_PG_DSN.
package pgoutbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgoutbox"
	"github.com/enrichment/waterfall/internal/tenant"
)

func TestPGOutbox_DeadLetterAfterMaxAttempts(t *testing.T) {
	cfg := dsn(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setup(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls"
	outbox, err := pgoutbox.Open(appCfg, 4) // tenant-scoped store (RLS enforced)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()

	ctxA := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A"})
	ctxB := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-B"})

	// A poison job: durably submitted, but we never run a worker to a terminal Put, so it stays
	// pending and is re-claimed every cycle — exactly the crash-loop that max-attempts guards.
	poison := &job.Job{
		ID:        "poison-1",
		TenantID:  "tenant-A",
		Principal: tenant.Principal{TenantID: "tenant-A"},
		Status:    job.StatusQueued,
		Req: domain.EnrichmentRequest{
			JobID:   "poison-1",
			Subject: domain.Subject{ID: "subj-1", Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"}},
			Want:    []domain.Field{domain.FieldWorkEmail},
		},
	}
	if ok, err := outbox.Submit(ctxA, poison); err != nil || !ok {
		t.Fatalf("submit poison: ok=%v err=%v", ok, err)
	}

	relayConn, err := pg.Connect(cfg) // privileged (superuser) claim connection
	if err != nil {
		t.Fatalf("connect relay: %v", err)
	}
	defer relayConn.Close()

	const maxAtt = 3
	var deadIDs []string
	queue := job.NewQueue(64) // we never drain it; the job just sits claimed+pending
	relay := pgoutbox.NewRelay(relayConn, queue, time.Millisecond,
		pgoutbox.WithMaxAttempts(maxAtt),
		pgoutbox.WithDeadLetterHook(func(id string, attempts int) { deadIDs = append(deadIDs, id) }),
	)

	// maxAtt deliveries, then the next claim parks it. Sleep exceeds the 1ms visibility so each
	// cycle re-claims the still-pending row.
	for i := 0; i < maxAtt+2; i++ {
		if _, err := relay.DrainOnce(); err != nil {
			t.Fatalf("drain %d: %v", i, err)
		}
		time.Sleep(4 * time.Millisecond)
	}

	// The hook fired exactly once, for our poison job.
	if len(deadIDs) != 1 || deadIDs[0] != "poison-1" {
		t.Fatalf("expected exactly one dead-letter for poison-1, got %v", deadIDs)
	}

	// The tenant-scoped DLQ read surfaces it, with attempts and a recorded reason.
	dls, err := outbox.DeadLetters(ctxA, 10)
	if err != nil {
		t.Fatalf("deadletters(A): %v", err)
	}
	if len(dls) != 1 || dls[0].JobID != "poison-1" {
		t.Fatalf("DLQ read should return poison-1, got %+v", dls)
	}
	if dls[0].Attempts <= maxAtt {
		t.Fatalf("dead-lettered attempts should exceed max (%d), got %d", maxAtt, dls[0].Attempts)
	}
	if dls[0].LastError == "" {
		t.Fatal("dead-letter must record a last_error reason")
	}

	// Parked rows are not re-claimed or re-dead-lettered.
	for i := 0; i < 3; i++ {
		if _, err := relay.DrainOnce(); err != nil {
			t.Fatalf("post-park drain: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(deadIDs) != 1 {
		t.Fatalf("a parked job must not be re-claimed; hook fired %d times", len(deadIDs))
	}

	// G1: another tenant does not see tenant-A's dead letters.
	if dlsB, err := outbox.DeadLetters(ctxB, 10); err != nil {
		t.Fatalf("deadletters(B): %v", err)
	} else if len(dlsB) != 0 {
		t.Fatalf("tenant-B must not see tenant-A's dead letters, got %d", len(dlsB))
	}
}
