package engine_test

import (
	"context"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
)

// ctxRecorder is a minimal Adapter that captures the context Fetch was invoked with, so a test can
// assert what attribution rode down to the provider/lease call site.
type ctxRecorder struct {
	field domain.Field
	value string
	seen  context.Context
}

func (r *ctxRecorder) Name() string { return "recorder" }
func (r *ctxRecorder) Capabilities() []provider.Capability {
	return []provider.Capability{{Field: r.field, Cost: 1, ExpectedConfidence: 0.9}}
}
func (r *ctxRecorder) Fetch(ctx context.Context, _ provider.Request) (provider.Result, error) {
	r.seen = ctx
	return provider.Result{Values: map[domain.Field]provider.Observation{
		r.field: {Value: r.value, Confidence: 0.9},
	}}, nil
}

// TestRun_ThreadsAttributionToLeaseCtx is the T5c/OI-P4-1b engine acceptance: when the caller tags
// the run with engine.WithAttribution, the workflow_key/country reach the provider-call context
// (where the egress AuthInjector draws the rotation lease) under the rotation attribution contract.
func TestRun_ThreadsAttributionToLeaseCtx(t *testing.T) {
	rec := &ctxRecorder{field: domain.FieldWorkEmail, value: "jane@acme.com"}
	st := store.NewMemory()
	eng := newEngine(st, rec)

	ctx := engine.WithAttribution(ctxFor("tenant-acme"), "enrich_email", "US")
	req := request("job-attr", "subj-attr", 0.99, 100, domain.FieldWorkEmail) // target unreachable => one call
	if _, err := eng.Run(ctx, req, router.New(rec).Plan(req)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rec.seen == nil {
		t.Fatal("adapter Fetch was never invoked")
	}
	wf, country := provider.AttributionFromContext(rec.seen)
	if wf != "enrich_email" || country != "US" {
		t.Fatalf("provider-call ctx attribution = (%q,%q), want (enrich_email,US)", wf, country)
	}
	t.Logf("PASS T5c: engine.WithAttribution threaded (workflow=%q,country=%q) onto the lease ctx", wf, country)
}

// TestRun_NoAttributionLeavesLeaseCtxEmpty pins backward compatibility: an unattributed run (no
// engine.WithAttribution — e.g. a dashboard-initiated / platform call) leaves the rotation
// attribution empty, so the usage row stays unattributed rather than mislabeled.
func TestRun_NoAttributionLeavesLeaseCtxEmpty(t *testing.T) {
	rec := &ctxRecorder{field: domain.FieldWorkEmail, value: "jane@acme.com"}
	st := store.NewMemory()
	eng := newEngine(st, rec)

	req := request("job-plain", "subj-plain", 0.99, 100, domain.FieldWorkEmail)
	if _, err := eng.Run(ctxFor("tenant-acme"), req, router.New(rec).Plan(req)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rec.seen == nil {
		t.Fatal("adapter Fetch was never invoked")
	}
	if wf, country := provider.AttributionFromContext(rec.seen); wf != "" || country != "" {
		t.Fatalf("unattributed run leaked attribution (%q,%q), want empty", wf, country)
	}
	t.Logf("PASS T5c: unattributed run keeps the lease ctx empty (platform/backward-compatible)")
}
