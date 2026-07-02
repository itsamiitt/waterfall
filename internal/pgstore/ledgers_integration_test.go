//go:build integration

// Live tests for the Postgres G2 (idempotency) and G4 (cost) ledgers, including tenant
// isolation on both — run as the non-superuser role app_rls (see rls_integration_test.go
// for shared setup helpers). Set WATERFALL_PG_DSN.
package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgstore"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

func openLedgerStore(t *testing.T) (*pgstore.Store, context.Context, context.Context) {
	t.Helper()
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls"
	st, err := pgstore.Open(appCfg, 4)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctxA := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-A"})
	ctxB := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-B"})
	return st, ctxA, ctxB
}

func TestPG_IdempotencyLedger(t *testing.T) {
	st, ctxA, ctxB := openLedgerStore(t)

	res := provider.Result{Values: map[domain.Field]provider.Observation{
		domain.FieldWorkEmail: {Value: "jane@acme.com", Confidence: 0.9},
	}}
	if err := st.Record(ctxA, "key-1", res); err != nil {
		t.Fatalf("record: %v", err)
	}

	got, ok, err := st.Lookup(ctxA, "key-1")
	if err != nil || !ok {
		t.Fatalf("lookup A: ok=%v err=%v", ok, err)
	}
	if got.Values[domain.FieldWorkEmail].Value != "jane@acme.com" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Tenant isolation (G1 on the G2 table): tenant-B cannot see tenant-A's ledger entry.
	if _, okB, _ := st.Lookup(ctxB, "key-1"); okB {
		t.Fatal("tenant-B must not see tenant-A's idempotency entry")
	}

	// First writer wins: a second Record with a different payload is a no-op.
	other := provider.Result{Values: map[domain.Field]provider.Observation{
		domain.FieldWorkEmail: {Value: "changed@evil.com", Confidence: 0.1},
	}}
	if err := st.Record(ctxA, "key-1", other); err != nil {
		t.Fatalf("second record: %v", err)
	}
	again, _, _ := st.Lookup(ctxA, "key-1")
	if again.Values[domain.FieldWorkEmail].Value != "jane@acme.com" {
		t.Fatal("first-writer-wins violated: ledger value was overwritten")
	}
}

func TestPG_CostLedger(t *testing.T) {
	st, ctxA, ctxB := openLedgerStore(t)
	const job = "job-1"

	// Reserve within the ceiling.
	if c, err := st.Reserve(ctxA, job, 5, 10); err != nil || c != 5 {
		t.Fatalf("reserve 5/10: committed=%d err=%v", c, err)
	}
	// A reservation that would exceed the ceiling is rejected and changes nothing.
	if _, err := st.Reserve(ctxA, job, 6, 10); !errors.Is(err, store.ErrCeilingExceeded) {
		t.Fatalf("reserve 6 more (=11>10) should be ErrCeilingExceeded, got %v", err)
	}
	if c, _ := st.Committed(ctxA, job); c != 5 {
		t.Fatalf("rejected reservation must not change committed, got %d", c)
	}
	// Reserve exactly up to the ceiling.
	if c, err := st.Reserve(ctxA, job, 5, 10); err != nil || c != 10 {
		t.Fatalf("reserve to 10: committed=%d err=%v", c, err)
	}
	// Release refunds.
	if err := st.Release(ctxA, job, 4); err != nil {
		t.Fatalf("release: %v", err)
	}
	if c, _ := st.Committed(ctxA, job); c != 6 {
		t.Fatalf("after release committed should be 6, got %d", c)
	}

	// Tenant isolation (G1 on the G4 table): tenant-B sees its own (empty) ledger.
	if c, _ := st.Committed(ctxB, job); c != 0 {
		t.Fatalf("tenant-B must not see tenant-A's committed spend, got %d", c)
	}
}
