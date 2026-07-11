package intent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

type fakeCollector struct {
	signals []Signal
	err     error
}

func (f fakeCollector) Collect(_ context.Context, _ string) ([]Signal, error) {
	return f.signals, f.err
}

type fakeScoreWriter struct {
	account string
	scores  []ClassScore
	config  string
}

func (f *fakeScoreWriter) SaveScores(_ context.Context, account, cfg string, scores []ClassScore) error {
	f.account, f.scores, f.config = account, scores, cfg
	return nil
}

type fakeFieldWriter struct {
	appended map[string][]domain.FieldValue
}

func (f *fakeFieldWriter) Append(_ context.Context, subjectID string, v domain.FieldValue) error {
	if f.appended == nil {
		f.appended = map[string][]domain.FieldValue{}
	}
	f.appended[subjectID] = append(f.appended[subjectID], v)
	return nil
}

func TestRefresh_PersistsScoresAndWritesBackFields(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	collector := fakeCollector{signals: []Signal{
		{Class: ClassHiring, Type: "eng_hiring", Magnitude: 1.0, ObservedAt: base, Confidence: 0.7}, // weight 0.8
		{Class: ClassBuying, Type: "funding", Magnitude: 1.0, ObservedAt: base, Confidence: 0.6},    // weight 0.6
	}}
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
	if len(scores) != 2 {
		t.Fatalf("scores = %d, want 2", len(scores))
	}
	// Scores persisted.
	if sw.account != "acme.com" || len(sw.scores) != 2 || sw.config != "intent-v1" {
		t.Fatalf("SaveScores got account=%q n=%d cfg=%q", sw.account, len(sw.scores), sw.config)
	}
	// Canonical Fields written back for the account.
	byField := map[domain.Field]string{}
	for _, v := range fw.appended["acme.com"] {
		byField[v.Field] = v.Value
		if v.Prov.Provider != writebackProvider || !v.Valid() {
			t.Fatalf("bad write-back provenance: %+v", v)
		}
	}
	if byField[domain.FieldIntentScore] == "" || byField[domain.FieldIntentTopics] == "" {
		t.Fatalf("write-back fields = %v", byField)
	}
	// hiring (0.8) beats buying (0.6) → buying_signal = hiring, topics score-desc.
	if byField[domain.FieldBuyingSignal] != "hiring" {
		t.Fatalf("buying_signal = %q, want hiring", byField[domain.FieldBuyingSignal])
	}
	if byField[domain.FieldIntentTopics] != "hiring,buying" {
		t.Fatalf("intent_topics = %q, want hiring,buying", byField[domain.FieldIntentTopics])
	}
}

func TestRefresh_NoSignalsWritesNothing(t *testing.T) {
	scorer := NewScorer(DefaultWeights())
	sw := &fakeScoreWriter{}
	fw := &fakeFieldWriter{}
	r := NewRefresher(fakeCollector{signals: nil}, scorer)
	r.Scores, r.Fields = sw, fw

	scores, err := r.Refresh(context.Background(), "empty.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 0 {
		t.Fatalf("scores = %d, want 0", len(scores))
	}
	if len(fw.appended["empty.com"]) != 0 {
		t.Fatalf("no Fields should be written for no signals, got %v", fw.appended)
	}
}

func TestRefresh_CollectorErrorPropagates(t *testing.T) {
	r := NewRefresher(fakeCollector{err: errors.New("boom")}, NewScorer(DefaultWeights()))
	if _, err := r.Refresh(context.Background(), "x"); err == nil {
		t.Fatal("collector error should propagate")
	}
}
