package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

func ctxFor(tenantID string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tenantID})
}

func goodValue(f domain.Field, val string, conf domain.Confidence) domain.FieldValue {
	return domain.FieldValue{
		Field:      f,
		Value:      val,
		Confidence: conf,
		Prov: domain.Provenance{
			Provider:       "acme",
			ObservedAt:     time.Unix(1700000000, 0),
			CostCredits:    3,
			Confidence:     conf,
			IdempotencyKey: "key-1",
		},
	}
}

// TestG1_CrossTenantIsolation is the RELEASE-BLOCKER negative test (docs/18 §1, docs/21):
// data written under tenant A must be invisible to tenant B across every ledger.
func TestG1_CrossTenantIsolation(t *testing.T) {
	m := NewMemory()
	a := ctxFor("tenant-A")
	b := ctxFor("tenant-B")

	// A writes a field version, an idempotency result, and a cost reservation.
	if err := m.Append(a, "subj-1", goodValue(domain.FieldWorkEmail, "a@acme.com", 0.9)); err != nil {
		t.Fatalf("A append: %v", err)
	}
	if err := m.Record(a, "idem-A", provider.Result{Values: map[domain.Field]provider.Observation{
		domain.FieldWorkEmail: {Value: "a@acme.com", Confidence: 0.9},
	}}); err != nil {
		t.Fatalf("A record: %v", err)
	}
	if _, err := m.Reserve(a, "job-A", 5, 100); err != nil {
		t.Fatalf("A reserve: %v", err)
	}

	// B must see NOTHING of A's.
	if cur, _ := m.Current(b, "subj-1"); len(cur) != 0 {
		t.Fatalf("G1 VIOLATION: tenant B read tenant A's field versions: %+v", cur)
	}
	if _, ok, _ := m.Lookup(b, "idem-A"); ok {
		t.Fatalf("G1 VIOLATION: tenant B read tenant A's idempotency result")
	}
	if c, _ := m.Committed(b, "job-A"); c != 0 {
		t.Fatalf("G1 VIOLATION: tenant B read tenant A's cost ledger: %d", c)
	}

	// And A still sees its own data (isolation is not amnesia).
	if cur, _ := m.Current(a, "subj-1"); len(cur) != 1 {
		t.Fatalf("tenant A lost its own data: %+v", cur)
	}
}

// TestG1_NoPrincipalFailsClosed proves store methods refuse to operate without a bound
// principal.
func TestG1_NoPrincipalFailsClosed(t *testing.T) {
	m := NewMemory()
	bare := context.Background()
	if err := m.Append(bare, "s", goodValue(domain.FieldWorkEmail, "x@y.com", 0.5)); !errors.Is(err, tenant.ErrNoPrincipal) {
		t.Fatalf("Append without principal must fail closed, got %v", err)
	}
	if _, _, err := m.Lookup(bare, "k"); !errors.Is(err, tenant.ErrNoPrincipal) {
		t.Fatalf("Lookup without principal must fail closed, got %v", err)
	}
	if _, err := m.Reserve(bare, "j", 1, 10); !errors.Is(err, tenant.ErrNoPrincipal) {
		t.Fatalf("Reserve without principal must fail closed, got %v", err)
	}
}

// TestG4_ReserveNeverExceedsCeiling proves the cost gate is atomic and hard.
func TestG4_ReserveNeverExceedsCeiling(t *testing.T) {
	m := NewMemory()
	ctx := ctxFor("t")
	if _, err := m.Reserve(ctx, "job", 60, 100); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	// 60 + 50 = 110 > 100 -> must be refused with no change.
	if _, err := m.Reserve(ctx, "job", 50, 100); !errors.Is(err, ErrCeilingExceeded) {
		t.Fatalf("want ErrCeilingExceeded, got %v", err)
	}
	if c, _ := m.Committed(ctx, "job"); c != 60 {
		t.Fatalf("committed changed on a refused reserve: %d", c)
	}
	// A fitting reservation still works, and a release refunds.
	if _, err := m.Reserve(ctx, "job", 40, 100); err != nil {
		t.Fatalf("fitting reserve: %v", err)
	}
	if c, _ := m.Committed(ctx, "job"); c != 100 {
		t.Fatalf("committed want 100, got %d", c)
	}
	if err := m.Release(ctx, "job", 40); err != nil {
		t.Fatalf("release: %v", err)
	}
	if c, _ := m.Committed(ctx, "job"); c != 60 {
		t.Fatalf("committed after release want 60, got %d", c)
	}
}

// TestG5_RejectBareValue proves the provenance gate: a value with no lineage cannot be
// persisted.
func TestG5_RejectBareValue(t *testing.T) {
	m := NewMemory()
	ctx := ctxFor("t")
	bare := domain.FieldValue{Field: domain.FieldWorkEmail, Value: "x@y.com"} // no Prov
	if err := m.Append(ctx, "s", bare); err == nil {
		t.Fatal("G5 VIOLATION: store accepted a value with no provenance")
	}
	// A non-canonical field is also rejected.
	badField := goodValue("not_a_field", "v", 0.5)
	if err := m.Append(ctx, "s", badField); err == nil {
		t.Fatal("store accepted a non-canonical field")
	}
}
