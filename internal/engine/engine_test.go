package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/calibrate"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

var fixedTime = time.Unix(1700000000, 0)

func ctxFor(tid string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tid})
}

func fastPolicy() provider.CallPolicy {
	return provider.CallPolicy{Timeout: time.Second, MaxAttempts: 1}
}

func newEngine(st store.Store, adapters ...provider.Adapter) *engine.Engine {
	return engine.New(st, adapters,
		engine.WithClock(func() time.Time { return fixedTime }),
		engine.WithPolicy(fastPolicy()),
	)
}

func request(jobID, subjectID string, target domain.Confidence, ceiling domain.Credits, want ...domain.Field) domain.EnrichmentRequest {
	return domain.EnrichmentRequest{
		JobID:            jobID,
		Subject:          domain.Subject{ID: subjectID, Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"}},
		Want:             want,
		ConfidenceTarget: target,
		CostCeiling:      ceiling,
		ConfigVersion:    "v1",
	}
}

// budgetCapturingAdapter wraps a Fake, implements provider.PolicyOverrider, and records the
// per-call deadline the engine actually granted — so a test can prove the override won.
type budgetCapturingAdapter struct {
	*providertest.Fake
	policy    provider.CallPolicy
	gotBudget time.Duration
}

func (b *budgetCapturingAdapter) CallPolicy() provider.CallPolicy { return b.policy }

func (b *budgetCapturingAdapter) Fetch(ctx context.Context, req provider.Request) (provider.Result, error) {
	if dl, ok := ctx.Deadline(); ok {
		b.gotBudget = time.Until(dl)
	}
	return b.Fake.Fetch(ctx, req)
}

// TestPolicyOverride_AsyncBudget proves ADR-0024 Phase 1: an adapter that implements
// PolicyOverrider with a longer Timeout is called under that budget, not the engine's
// (short) default — while a plain adapter keeps the default.
func TestPolicyOverride_AsyncBudget(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	fake := providertest.New("slowco", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	a := &budgetCapturingAdapter{
		Fake:   fake,
		policy: provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
	}
	// Engine default is the 1s fastPolicy; the override must win.
	eng := newEngine(st, a)

	req := request("jobP", "subjP", 0.85, 100, domain.FieldWorkEmail)
	plan := router.New(a).Plan(req)
	if _, err := eng.Run(ctx, req, plan); err != nil {
		t.Fatalf("run: %v", err)
	}
	if a.gotBudget < 5*time.Second {
		t.Fatalf("policy override not honored: per-call budget was %s, want ~30s (>5s)", a.gotBudget)
	}
}

// TestPolicyOverride_ZeroKeepsDefault proves an HTTPAdapter with no Policy (the zero
// CallPolicy) does NOT override the engine default — the backward-compatible path.
func TestPolicyOverride_ZeroKeepsDefault(t *testing.T) {
	var h provider.Adapter = &provider.HTTPAdapter{NameV: "x"}
	po, ok := h.(provider.PolicyOverrider)
	if !ok {
		t.Fatal("HTTPAdapter should implement PolicyOverrider")
	}
	if got := po.CallPolicy(); got.Timeout != 0 {
		t.Fatalf("unset Policy should yield zero (no override), got Timeout=%s", got.Timeout)
	}
}

// TestG5_HappyPathRecordsProvenance proves a filled value carries complete provenance
// and the charge is accounted.
func TestG5_HappyPathRecordsProvenance(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	a := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := newEngine(st, a)

	req := request("job1", "subj1", 0.85, 100, domain.FieldWorkEmail)
	plan := router.New(a).Plan(req)
	out, err := eng.Run(ctx, req, plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	fv, ok := out.Filled[domain.FieldWorkEmail]
	if !ok {
		t.Fatal("work_email not filled")
	}
	if fv.Value != "jane@acme.com" {
		t.Fatalf("value: %q", fv.Value)
	}
	if !fv.Valid() {
		t.Fatal("G5 VIOLATION: filled value fails validity")
	}
	if fv.Prov.Provider != "acme" || fv.Prov.IdempotencyKey == "" || fv.Prov.ObservedAt.IsZero() {
		t.Fatalf("G5 VIOLATION: incomplete provenance: %+v", fv.Prov)
	}
	if fv.Prov.CostCredits != 5 {
		t.Fatalf("provenance cost want 5, got %d", fv.Prov.CostCredits)
	}
	if out.Committed != 5 {
		t.Fatalf("committed want 5, got %d", out.Committed)
	}
	if out.Stops[domain.FieldWorkEmail] != engine.StopTargetMet {
		t.Fatalf("stop reason want target-met, got %s", out.Stops[domain.FieldWorkEmail])
	}
}

// TestG2_ReplayNoDoubleChargeOrCall proves idempotency: a second identical run makes no
// new provider call and no new charge.
func TestG2_ReplayNoDoubleChargeOrCall(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	a := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := newEngine(st, a)
	req := request("job1", "subj1", 0.99, 100, domain.FieldWorkEmail) // target unreachable => provider tried once
	plan := router.New(a).Plan(req)

	if _, err := eng.Run(ctx, req, plan); err != nil {
		t.Fatalf("run1: %v", err)
	}
	if _, err := eng.Run(ctx, req, plan); err != nil {
		t.Fatalf("run2: %v", err)
	}

	if a.Calls() != 1 {
		t.Fatalf("G2 VIOLATION: provider called %d times across two identical runs (want 1)", a.Calls())
	}
	committed, _ := st.Committed(ctx, "job1")
	if committed != 5 {
		t.Fatalf("G2 VIOLATION: charged %d credits (want 5 — no double charge)", committed)
	}
}

// TestG4_CeilingStopsSpending proves cost can never exceed the per-record ceiling: with a
// ceiling that affords only one call, the second field is not paid for.
func TestG4_CeilingStopsSpending(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	email := providertest.New("emailco", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	phone := providertest.New("phoneco", "+15551234", 0.9, 5, domain.FieldMobilePhone)
	eng := newEngine(st, email, phone)

	// Ceiling 5 affords exactly one 5-credit call.
	req := request("job1", "subj1", 0.8, 5, domain.FieldWorkEmail, domain.FieldMobilePhone)
	plan := router.New(email, phone).Plan(req)
	out, err := eng.Run(ctx, req, plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if out.Committed > 5 {
		t.Fatalf("G4 VIOLATION: committed %d exceeds ceiling 5", out.Committed)
	}
	if len(out.Filled) != 1 {
		t.Fatalf("want exactly one field filled under the ceiling, got %d", len(out.Filled))
	}
	// Exactly one field should report the ceiling stop.
	ceilingStops := 0
	for _, r := range out.Stops {
		if r == engine.StopCeiling {
			ceilingStops++
		}
	}
	if ceilingStops != 1 {
		t.Fatalf("want one field stopped by ceiling, got %d (stops=%v)", ceilingStops, out.Stops)
	}
}

// TestFusion_TargetMetStopsEarly proves the sequential stop: once two agreeing providers
// push fused confidence past the target, no further provider is called.
func TestFusion_TargetMetStopsEarly(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	// Three providers, identical density+cost => deterministic name order a,b,c.
	a := providertest.New("a", "jane@acme.com", 0.8, 1, domain.FieldWorkEmail)
	b := providertest.New("b", "jane@acme.com", 0.8, 1, domain.FieldWorkEmail)
	c := providertest.New("c", "jane@acme.com", 0.8, 1, domain.FieldWorkEmail)
	eng := newEngine(st, a, b, c)

	req := request("job1", "subj1", 0.9, 100, domain.FieldWorkEmail)
	plan := router.New(a, b, c).Plan(req)
	out, err := eng.Run(ctx, req, plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if a.Calls() != 1 || b.Calls() != 1 {
		t.Fatalf("expected a and b to be called once each, got a=%d b=%d", a.Calls(), b.Calls())
	}
	if c.Calls() != 0 {
		t.Fatalf("target was met after two providers; c must NOT be called, got %d", c.Calls())
	}
	if out.Stops[domain.FieldWorkEmail] != engine.StopTargetMet {
		t.Fatalf("want target-met, got %s", out.Stops[domain.FieldWorkEmail])
	}
	if fv := out.Filled[domain.FieldWorkEmail]; fv.Confidence < 0.9 {
		t.Fatalf("fused confidence should exceed target, got %v", fv.Confidence)
	}
}

// TestFailover_RefundsFailedCall proves charge-on-success: a failing provider is not
// billed, and the engine fails over to the next provider.
func TestFailover_RefundsFailedCall(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	// down has higher density (cheaper) so it is tried first, then fails.
	down := providertest.New("down", "x", 0.9, 1, domain.FieldWorkEmail)
	down.Err = domain.NewProviderError("down", domain.ClassProviderDown, errors.New("outage"))
	up := providertest.New("up", "jane@acme.com", 0.9, 2, domain.FieldWorkEmail)
	eng := newEngine(st, down, up)

	req := request("job1", "subj1", 0.85, 100, domain.FieldWorkEmail)
	plan := router.New(down, up).Plan(req)
	out, err := eng.Run(ctx, req, plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	fv := out.Filled[domain.FieldWorkEmail]
	if fv.Prov.Provider != "up" {
		t.Fatalf("expected failover to 'up', got provider %q", fv.Prov.Provider)
	}
	if out.Committed != 2 {
		t.Fatalf("charge-on-success: only the successful provider (cost 2) should be billed, got %d", out.Committed)
	}
}

// TestCalibration_AppliedBeforeFusion proves calibrated confidence (not the raw provider
// score) drives the resolved value, while provenance keeps the raw score.
func TestCalibration_AppliedBeforeFusion(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	cal := calibrate.New()
	// (acme, work_email): raw 0.9 -> ~0.33 (right 1 of 3 times).
	cal.Fit("acme", domain.FieldWorkEmail, []calibrate.Point{{X: 0.9, Y: 1}, {X: 0.9, Y: 0}, {X: 0.9, Y: 0}})

	a := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := engine.New(st, []provider.Adapter{a},
		engine.WithClock(func() time.Time { return fixedTime }),
		engine.WithPolicy(fastPolicy()),
		engine.WithCalibrator(cal),
	)
	req := request("job1", "subj1", 0.8, 100, domain.FieldWorkEmail)
	out, err := eng.Run(ctx, req, router.New(a).Plan(req))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	fv := out.Filled[domain.FieldWorkEmail]
	if fv.Confidence < 0.25 || fv.Confidence > 0.45 {
		t.Fatalf("resolved confidence should be the calibrated ~0.33, got %.3f", fv.Confidence)
	}
	if fv.Prov.Confidence != 0.9 {
		t.Fatalf("provenance must keep the RAW provider score 0.9, got %.3f", fv.Prov.Confidence)
	}
	// Because calibrated 0.33 < target 0.8, the field is filled but not "target-met".
	if out.Stops[domain.FieldWorkEmail] == engine.StopTargetMet {
		t.Fatal("calibrated confidence below target should not report target-met")
	}
}

// TestBandit_LearnsBetterProvider proves the closed loop: the engine updates the bandit
// after real calls, and over many records the reliable provider's posterior overtakes the
// failing one — while the deterministic gates are untouched.
func TestBandit_LearnsBetterProvider(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	good := providertest.New("good", "jane@acme.com", 0.9, 1, domain.FieldWorkEmail)
	bad := providertest.New("bad", "x", 0.9, 1, domain.FieldWorkEmail)
	bad.Err = domain.NewProviderError("bad", domain.ClassTransient, errors.New("down"))

	b := bandit.New()
	eng := engine.New(st, []provider.Adapter{good, bad},
		engine.WithClock(func() time.Time { return fixedTime }),
		engine.WithPolicy(provider.CallPolicy{Timeout: time.Second, MaxAttempts: 1}),
		engine.WithBandit(b),
	)
	planner := router.New(good, bad)

	for i := 0; i < 40; i++ {
		req := request("job", "subj-"+string(rune('a'+i%26))+string(rune('0'+i/26)), 0.85, 100, domain.FieldWorkEmail)
		plan := planner.WithScorer(b.NewScorer(int64(i + 1))).Plan(req)
		if _, err := eng.Run(ctx, req, plan); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	mg := b.Mean("good", domain.FieldWorkEmail)
	mb := b.Mean("bad", domain.FieldWorkEmail)
	if mg <= mb {
		t.Fatalf("bandit did not learn: mean(good)=%.3f should exceed mean(bad)=%.3f", mg, mb)
	}
	if mg < 0.6 {
		t.Fatalf("reliable provider posterior should be high, got %.3f", mg)
	}
	if mb > 0.5 {
		t.Fatalf("failing provider posterior should be low, got %.3f", mb)
	}
}

// TestMetrics_ProviderCallsAndFields proves the engine emits provider + enrichment metrics.
func TestMetrics_ProviderCallsAndFields(t *testing.T) {
	ctx := ctxFor("t1")
	st := store.NewMemory()
	reg := metrics.New()
	a := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := engine.New(st, []provider.Adapter{a},
		engine.WithClock(func() time.Time { return fixedTime }),
		engine.WithPolicy(fastPolicy()),
		engine.WithMetrics(reg),
	)
	req := request("job1", "subj1", 0.85, 100, domain.FieldWorkEmail)
	if _, err := eng.Run(ctx, req, router.New(a).Plan(req)); err != nil {
		t.Fatalf("run: %v", err)
	}

	var sb strings.Builder
	reg.Render(&sb)
	out := sb.String()
	if !strings.Contains(out, `provider_calls_total{provider="acme",result="success"} 1`) {
		t.Errorf("missing provider success metric:\n%s", out)
	}
	if !strings.Contains(out, `enrichment_fields_filled_total{field="work_email",provider="acme"} 1`) {
		t.Errorf("missing fields-filled metric:\n%s", out)
	}
	if !strings.Contains(out, `provider_cost_credits_total{provider="acme"} 5`) {
		t.Errorf("missing cost metric:\n%s", out)
	}
}

// TestG1_TenantScopedResults proves two tenants running the same job id/subject id do not
// share results.
func TestG1_TenantScopedResults(t *testing.T) {
	st := store.NewMemory()
	a := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	eng := newEngine(st, a)
	req := request("job1", "subj1", 0.99, 100, domain.FieldWorkEmail)
	plan := router.New(a).Plan(req)

	if _, err := eng.Run(ctxFor("tenantA"), req, plan); err != nil {
		t.Fatalf("A run: %v", err)
	}
	// Tenant B runs the SAME ids; it must not see A's cached result and must make its own call.
	if _, err := eng.Run(ctxFor("tenantB"), req, plan); err != nil {
		t.Fatalf("B run: %v", err)
	}
	if a.Calls() != 2 {
		t.Fatalf("G1: identical ids across tenants must not share the idempotency ledger; want 2 calls, got %d", a.Calls())
	}
	bCommitted, _ := st.Committed(ctxFor("tenantB"), "job1")
	if bCommitted != 5 {
		t.Fatalf("tenant B should have its own 5-credit charge, got %d", bCommitted)
	}
}
