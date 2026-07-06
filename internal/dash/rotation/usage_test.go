package rotation

import (
	"context"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

// fakeOpener returns a fixed plaintext secret without any crypto backend — unit tests exercise
// the lease/attribution path, never the wire, so no secret material is real.
type fakeOpener struct{}

func (fakeOpener) Open(_ context.Context, _ string) (string, error) { return "unit-secret", nil }

// seedPool builds a single-key unlimited pool (daily_limit 0 => the leaser is never consulted, so
// no Store is needed) and installs it in the engine's cache directly (white-box).
func seedPool(e *Engine, selector, keyID string, costPerCall int64) {
	rows := []poolKeyRow{{
		ID: keyID, EnvelopeID: "env-" + keyID, Status: "active",
		DailyLimit: i64p(0), CostPerCall: i64p(costPerCall),
	}}
	e.pools[selector] = buildPoolState(selector, "round_robin", "", rows, e.bandit)
}

// TestLeaseRecordUsage is OI-P4-1's unit acceptance: a completed lease invokes Config.RecordUsage
// EXACTLY ONCE, carrying the right key_id / provider_id / outcome plus the credits (cost_per_call),
// latency, and the ctx-captured tenant / workflow_key / country dimensions.
func TestLeaseRecordUsage(t *testing.T) {
	var got []UsageSample
	e := New(Config{
		Secrets:     fakeOpener{},
		RecordUsage: func(ev UsageSample) { got = append(got, ev) },
	})
	seedPool(e, "hunter:default", "key-1", 6)

	ctx := tenant.WithPrincipal(context.Background(),
		tenant.Principal{TenantID: "tenant-acme", Scopes: []string{"role:tenant_admin"}})
	ctx = WithWorkflowKey(ctx, "email_enrich")
	ctx = WithCountry(ctx, "US")

	lease, err := e.Lease(ctx, "hunter:default")
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if lease.KeyID != "key-1" {
		t.Fatalf("lease attributed to %q, want key-1", lease.KeyID)
	}

	// Not emitted until the call completes.
	if len(got) != 0 {
		t.Fatalf("RecordUsage fired before Done: %+v", got)
	}
	lease.Done(provider.Outcome{Class: domain.ClassUnknown, LatencyMs: 42, OK: true})

	if len(got) != 1 {
		t.Fatalf("RecordUsage fired %d times, want exactly 1", len(got))
	}
	want := UsageSample{
		TenantID: "tenant-acme", KeyID: "key-1", ProviderID: "hunter",
		WorkflowKey: "email_enrich", Country: "US", OutcomeClass: "ok", Credits: 6, LatMs: 42,
	}
	if got[0] != want {
		t.Fatalf("UsageSample = %+v, want %+v", got[0], want)
	}
	t.Logf("PASS OI-P4-1: completed lease emitted UsageSample %+v", got[0])
}

// TestLeaseRecordUsage_PlatformDefaultAndFailure pins two behaviors: a lease with NO authenticated
// principal (a dashboard-initiated health/test/bench call) attributes to the platform Tenant, and a
// failed Outcome maps to its taxonomy class string. ClassNotFound drives no KM-3 transition, so the
// path needs no Store.
func TestLeaseRecordUsage_PlatformDefaultAndFailure(t *testing.T) {
	var got UsageSample
	var n int
	e := New(Config{
		Secrets:     fakeOpener{},
		RecordUsage: func(ev UsageSample) { got = ev; n++ },
	})
	seedPool(e, "twilio:pool", "k9", 3)

	lease, err := e.Lease(context.Background(), "twilio:pool") // no principal on the ctx
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	lease.Done(provider.Outcome{Class: domain.ClassNotFound, LatencyMs: 7, OK: false})

	if n != 1 {
		t.Fatalf("RecordUsage fired %d times, want 1", n)
	}
	if got.TenantID != platformTenant {
		t.Fatalf("tenant = %q, want %q (dashboard-initiated default)", got.TenantID, platformTenant)
	}
	if got.OutcomeClass != "NOT_FOUND" {
		t.Fatalf("outcome_class = %q, want NOT_FOUND", got.OutcomeClass)
	}
	if got.ProviderID != "twilio" || got.KeyID != "k9" || got.Credits != 3 || got.LatMs != 7 {
		t.Fatalf("dimensions wrong: %+v", got)
	}
	if got.WorkflowKey != "" || got.Country != "" {
		t.Fatalf("unset workflow/country should stay empty: %+v", got)
	}
	t.Logf("PASS platform default + failure-class mapping: %+v", got)
}

// TestLeaseNilHookBackwardCompatible proves a nil RecordUsage is the prior behavior: Lease and
// Done work and never panic (no feed).
func TestLeaseNilHookBackwardCompatible(t *testing.T) {
	e := New(Config{Secrets: fakeOpener{}}) // RecordUsage nil
	seedPool(e, "hunter:default", "key-1", 5)

	lease, err := e.Lease(context.Background(), "hunter:default")
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	lease.Done(provider.Outcome{OK: true, LatencyMs: 3}) // must not panic
	t.Log("PASS nil hook: lease + done are unchanged (no usage feed)")
}

// TestOutcomeClass pins the Outcome -> outcome_class mapping across the taxonomy.
func TestOutcomeClass(t *testing.T) {
	cases := []struct {
		o    provider.Outcome
		want string
	}{
		{provider.Outcome{OK: true, Class: domain.ClassUnknown}, "ok"},
		{provider.Outcome{OK: true, Class: domain.ClassNotFound}, "ok"}, // OK wins regardless of class
		{provider.Outcome{OK: false, Class: domain.ClassAuth}, "AUTH"},
		{provider.Outcome{OK: false, Class: domain.ClassRateLimit}, "RATE_LIMIT"},
		{provider.Outcome{OK: false, Class: domain.ClassQuota}, "QUOTA"},
		{provider.Outcome{OK: false, Class: domain.ClassProviderDown}, "PROVIDER_DOWN"},
		{provider.Outcome{OK: false, Class: domain.ClassUnknown}, "UNKNOWN"},
	}
	for _, c := range cases {
		if got := outcomeClass(c.o); got != c.want {
			t.Fatalf("outcomeClass(%+v) = %q, want %q", c.o, got, c.want)
		}
	}
}
