package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/tenant"
)

func ctxFor(tid string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tid})
}

func fixedOutcome() engine.Outcome {
	return engine.Outcome{
		Committed: 7,
		Filled: map[domain.Field]domain.FieldValue{
			domain.FieldWorkEmail: {
				Field: domain.FieldWorkEmail, Value: "a@b.com", Confidence: 0.9,
				Prov: domain.Provenance{Provider: "p", ObservedAt: time.Unix(1, 0), IdempotencyKey: "k"},
			},
		},
		Stops: map[domain.Field]engine.StopReason{domain.FieldWorkEmail: engine.StopTargetMet},
	}
}

func newJob(tid, id string, prio Priority) *Job {
	return &Job{
		ID:        id,
		TenantID:  tid,
		Principal: tenant.Principal{TenantID: tid},
		Req:       domain.EnrichmentRequest{JobID: id, Subject: domain.Subject{ID: "s"}},
		Priority:  prio,
		Status:    StatusQueued,
	}
}

func TestDispatcher_RunMarksSucceeded(t *testing.T) {
	jobs := NewMemoryStore()
	run := func(ctx context.Context, _ domain.EnrichmentRequest) (engine.Outcome, error) {
		// The runner must see the job's tenant principal (G1).
		if tid, _ := tenant.TenantID(ctx); tid != "t1" {
			t.Errorf("runner got wrong tenant: %q", tid)
		}
		return fixedOutcome(), nil
	}
	d := NewDispatcher(NewQueue(1), jobs, run)
	j := newJob("t1", "job1", PriorityBulk)

	d.Run(j)

	got, ok, _ := jobs.Get(ctxFor("t1"), "job1")
	if !ok || got.Status != StatusSucceeded {
		t.Fatalf("want succeeded, got %+v", got)
	}
	if got.Outcome == nil || got.Outcome.Committed != 7 {
		t.Fatalf("outcome not recorded: %+v", got.Outcome)
	}
}

func TestDispatcher_RunErrorMarksFailed(t *testing.T) {
	jobs := NewMemoryStore()
	run := func(context.Context, domain.EnrichmentRequest) (engine.Outcome, error) {
		return engine.Outcome{}, errors.New("provider blew up")
	}
	d := NewDispatcher(NewQueue(1), jobs, run)
	j := newJob("t1", "job1", PriorityBulk)
	d.Run(j)
	got, _, _ := jobs.Get(ctxFor("t1"), "job1")
	if got.Status != StatusFailed || got.Err == "" {
		t.Fatalf("want failed with error, got %+v", got)
	}
}

func TestDispatcher_PanicRecovered(t *testing.T) {
	jobs := NewMemoryStore()
	run := func(context.Context, domain.EnrichmentRequest) (engine.Outcome, error) {
		panic("boom")
	}
	d := NewDispatcher(NewQueue(1), jobs, run)
	j := newJob("t1", "job1", PriorityBulk)
	d.Run(j) // must not crash the test process
	got, _, _ := jobs.Get(ctxFor("t1"), "job1")
	if got.Status != StatusFailed {
		t.Fatalf("panic should mark job failed, got %+v", got)
	}
}

func TestDispatcher_AsyncViaQueue(t *testing.T) {
	jobs := NewMemoryStore()
	q := NewQueue(8)
	run := func(context.Context, domain.EnrichmentRequest) (engine.Outcome, error) {
		return fixedOutcome(), nil
	}
	d := NewDispatcher(q, jobs, run)
	d.Start(4)
	defer d.Stop()

	j := newJob("t1", "job1", PriorityPremium)
	if err := jobs.Put(ctxFor("t1"), j); err != nil {
		t.Fatal(err)
	}
	if !q.Submit(j) {
		t.Fatal("submit should succeed")
	}

	// Poll until the worker completes the job.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _, _ := jobs.Get(ctxFor("t1"), "job1")
		if got != nil && got.Status == StatusSucceeded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete in time: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestJobStore_TenantIsolation(t *testing.T) {
	jobs := NewMemoryStore()
	_ = jobs.Put(ctxFor("A"), newJob("A", "job1", PriorityBulk))
	if _, ok, _ := jobs.Get(ctxFor("B"), "job1"); ok {
		t.Fatal("G1 VIOLATION: tenant B read tenant A's job")
	}
	if _, ok, _ := jobs.Get(ctxFor("A"), "job1"); !ok {
		t.Fatal("tenant A should see its own job")
	}
}

func TestDeriveID_DeterministicAndScoped(t *testing.T) {
	if DeriveID("t1", "k1") != DeriveID("t1", "k1") {
		t.Fatal("same tenant+key must derive the same id")
	}
	if DeriveID("t1", "k1") == DeriveID("t2", "k1") {
		t.Fatal("different tenants must derive different ids for the same key")
	}
	if DeriveID("t1", "k1") == DeriveID("t1", "k2") {
		t.Fatal("different keys must derive different ids")
	}
}
