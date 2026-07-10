package intent

import (
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
)

func TestProject_TopClassAndTopics(t *testing.T) {
	w := Project([]ClassScore{
		{Class: ClassBuying, Score: 0.6, Confidence: 0.5},
		{Class: ClassHiring, Score: 0.8, Confidence: 0.7},
	}, 0.5)
	if w.IntentScore != "0.800" {
		t.Fatalf("intent_score = %q, want 0.800", w.IntentScore)
	}
	if w.BuyingSignal != "hiring" {
		t.Fatalf("buying_signal = %q, want hiring (strongest class)", w.BuyingSignal)
	}
	if w.IntentTopics != "hiring,buying" {
		t.Fatalf("intent_topics = %q, want hiring,buying (score desc)", w.IntentTopics)
	}
	if w.TopClass != ClassHiring || w.Confidence != 0.7 {
		t.Fatalf("top=%v conf=%v", w.TopClass, w.Confidence)
	}
}

func TestProject_MinTopicFilters(t *testing.T) {
	w := Project([]ClassScore{
		{Class: ClassHiring, Score: 0.8},
		{Class: ClassBuying, Score: 0.6},
	}, 0.7)
	if w.IntentTopics != "hiring" {
		t.Fatalf("intent_topics = %q, want hiring (buying below threshold)", w.IntentTopics)
	}
	// intent_score + buying_signal still reflect the strongest class even when topics is filtered.
	if w.BuyingSignal != "hiring" || w.IntentScore != "0.800" {
		t.Fatalf("top signal=%q score=%q", w.BuyingSignal, w.IntentScore)
	}
}

func TestProject_EmptyIsZero(t *testing.T) {
	w := Project(nil, 0.5)
	if w != (Writeback{}) {
		t.Fatalf("empty projection should be zero, got %+v", w)
	}
	if len(w.Fields()) != 0 {
		t.Fatalf("empty Fields() should be empty, got %v", w.Fields())
	}
}

func TestWriteback_Fields(t *testing.T) {
	f := Project([]ClassScore{{Class: ClassHiring, Score: 0.8}}, 0.5).Fields()
	if f[domain.FieldIntentScore] != "0.800" ||
		f[domain.FieldBuyingSignal] != "hiring" ||
		f[domain.FieldIntentTopics] != "hiring" {
		t.Fatalf("fields = %v", f)
	}
	if len(f) != 3 {
		t.Fatalf("expected exactly the 3 intent Fields, got %d: %v", len(f), f)
	}
}
