package telemetry

import (
	"context"
	"testing"
)

// TestBufferedRecorderOverflowDrop proves the hot-path back-pressure contract: with the flusher
// NOT running, a full bounded queue drops further events (counted), and Record never blocks. A
// telemetry stall must never wedge enrichment (doc 12 §P4, doc 10 §2).
func TestBufferedRecorderOverflowDrop(t *testing.T) {
	// Capacity 2, no Start() -> nothing drains the channel.
	b := NewBufferedRecorder(nil, BufferedConfig{Capacity: 2, BatchMax: 4}, nil)

	ctx := context.Background()
	ev := UsageEvent{TenantID: "acme", ProviderID: "hunter", OutcomeClass: OutcomeOK}

	// First 2 accepted (fill the buffer), next 3 dropped.
	for i := 0; i < 5; i++ {
		b.Record(ctx, ev)
	}
	if got := b.Dropped(); got != 3 {
		t.Fatalf("dropped = %d, want 3 (capacity 2, 5 offered)", got)
	}

	// Draining one slot lets exactly one more in.
	<-b.ch
	b.Record(ctx, ev)
	if got := b.Dropped(); got != 3 {
		t.Fatalf("dropped after draining one = %d, want still 3", got)
	}
	b.Record(ctx, ev)
	if got := b.Dropped(); got != 4 {
		t.Fatalf("dropped after re-filling = %d, want 4", got)
	}
}

// TestNopRecorderSatisfiesSink is a compile-time + runtime check that both fire-and-forget
// recorders satisfy Sink and never panic.
func TestNopRecorderSatisfiesSink(t *testing.T) {
	var sinks []Sink = []Sink{NopRecorder{}, NewBufferedRecorder(nil, BufferedConfig{}, nil)}
	for _, s := range sinks {
		s.Record(context.Background(), UsageEvent{TenantID: "acme", ProviderID: "p", OutcomeClass: OutcomeOK})
	}
}
