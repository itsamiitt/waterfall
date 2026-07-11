package intent

import (
	"context"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

type fakeFieldReader struct {
	cur map[domain.Field]domain.FieldValue
	err error
}

func (f fakeFieldReader) Current(_ context.Context, _ string) (map[domain.Field]domain.FieldValue, error) {
	return f.cur, f.err
}

func TestEngineCollector_DerivesSignalsFromFields(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	fr := fakeFieldReader{cur: map[domain.Field]domain.FieldValue{
		domain.FieldBuyingSignal: {Field: domain.FieldBuyingSignal, Value: "hiring", Confidence: 0.7,
			Prov: domain.Provenance{Provider: "predictleads"}},
		domain.FieldFundingStage: {Field: domain.FieldFundingStage, Value: "series_a", Confidence: 0.8,
			Prov: domain.Provenance{Provider: "crunchbase"}},
	}}
	c := &EngineSignalCollector{Fields: fr, now: func() time.Time { return base }}

	sigs, err := c.Collect(context.Background(), "acme.com")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("signals = %d, want 2: %+v", len(sigs), sigs)
	}
	byClass := map[Class]Signal{}
	for _, s := range sigs {
		byClass[s.Class] = s
		if s.SourceType != SourceAPI || s.Account != "acme.com" || s.ObservedAt != base {
			t.Fatalf("signal shape wrong: %+v", s)
		}
	}
	if h, ok := byClass[ClassHiring]; !ok || h.Type != "job_posting" || h.Provider != "predictleads" {
		t.Fatalf("hiring signal = %+v", h)
	}
	if b, ok := byClass[ClassBuying]; !ok || b.Type != "funding" || b.Provider != "crunchbase" {
		t.Fatalf("buying signal = %+v", b)
	}
}

func TestEngineCollector_UnknownBuyingSignalYieldsNothing(t *testing.T) {
	c := &EngineSignalCollector{Fields: fakeFieldReader{cur: map[domain.Field]domain.FieldValue{
		domain.FieldBuyingSignal: {Field: domain.FieldBuyingSignal, Value: "some_unmapped_event"},
	}}}
	sigs, err := c.Collect(context.Background(), "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 0 {
		t.Fatalf("unknown buying_signal must yield no signal, got %+v", sigs)
	}
}

func TestEngineCollector_EmptyFields(t *testing.T) {
	c := &EngineSignalCollector{Fields: fakeFieldReader{cur: map[domain.Field]domain.FieldValue{}}}
	sigs, err := c.Collect(context.Background(), "acme.com")
	if err != nil || len(sigs) != 0 {
		t.Fatalf("no fields → no signals; got %d err=%v", len(sigs), err)
	}
}

// TestEngineCollector_FeedsRefresher wires the collector into the refresher end-to-end (fakes),
// proving the full intent pipeline runs from enrichment Fields → scores → write-back.
func TestEngineCollector_FeedsRefresher(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	fr := fakeFieldReader{cur: map[domain.Field]domain.FieldValue{
		domain.FieldBuyingSignal: {Field: domain.FieldBuyingSignal, Value: "hiring", Confidence: 0.7,
			Prov: domain.Provenance{Provider: "predictleads"}},
	}}
	collector := &EngineSignalCollector{Fields: fr, now: func() time.Time { return base }}
	scorer := NewScorer(DefaultWeights())
	scorer.now = func() time.Time { return base }
	sw := &fakeScoreWriter{}
	fw := &fakeFieldWriter{}
	r := NewRefresher(collector, scorer)
	r.Scores, r.Fields = sw, fw
	r.now = func() time.Time { return base }

	scores, err := r.Refresh(context.Background(), "acme.com")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(scores) != 1 || scores[0].Class != ClassHiring {
		t.Fatalf("scores = %+v, want one hiring", scores)
	}
	// The hiring intent was written back to the canonical Fields.
	var sawIntent bool
	for _, v := range fw.appended["acme.com"] {
		if v.Field == domain.FieldBuyingSignal && v.Value == "hiring" {
			sawIntent = true
		}
	}
	if !sawIntent {
		t.Fatalf("expected hiring written back to buying_signal, got %+v", fw.appended["acme.com"])
	}
}
