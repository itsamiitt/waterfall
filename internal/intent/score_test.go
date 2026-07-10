package intent

import (
	"math"
	"testing"
	"time"
)

func at(base time.Time, ago time.Duration) time.Time { return base.Add(-ago) }

func fixedScorer() (*Scorer, time.Time) {
	base := time.Unix(1_700_000_000, 0).UTC()
	s := NewScorer(DefaultWeights())
	s.now = func() time.Time { return base }
	return s, base
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScore_SingleSignalIsWeightedMagnitude(t *testing.T) {
	s, base := fixedScorer()
	// job_posting weight 0.6, magnitude 1.0, fresh (age 0) → weighted 0.6 → noisy-OR of 1 term = 0.6.
	cs := s.Score(ClassHiring, []Signal{
		{Class: ClassHiring, Type: "job_posting", Magnitude: 1.0, ObservedAt: base, Provider: "theirstack", SourceType: SourceAPI, Confidence: 0.8},
	})
	if !approx(cs.Score, 0.6) {
		t.Fatalf("score = %v, want 0.6", cs.Score)
	}
	if cs.SignalCount != 1 || len(cs.Reasoning) != 1 {
		t.Fatalf("signal_count=%d reasoning=%d", cs.SignalCount, len(cs.Reasoning))
	}
	if cs.Confidence <= 0 {
		t.Fatalf("confidence should be > 0 with a signal, got %v", cs.Confidence)
	}
}

func TestScore_FreshnessDecayHalvesAtHalfLife(t *testing.T) {
	s, base := fixedScorer()
	// job_posting half-life is 30 days; a signal one half-life old decays to 0.5 magnitude.
	cs := s.Score(ClassHiring, []Signal{
		{Class: ClassHiring, Type: "job_posting", Magnitude: 1.0, ObservedAt: at(base, 30*24*time.Hour), Provider: "p"},
	})
	// weighted = 0.5 (decayed) × 0.6 (weight) = 0.3 → score 0.3.
	if !approx(cs.Score, 0.3) {
		t.Fatalf("decayed score = %v, want 0.3", cs.Score)
	}
	if !approx(cs.Reasoning[0].Decayed, 0.5) {
		t.Fatalf("decayed magnitude = %v, want 0.5", cs.Reasoning[0].Decayed)
	}
}

func TestScore_CorroborationRaisesScore(t *testing.T) {
	s, base := fixedScorer()
	one := s.Score(ClassHiring, []Signal{
		{Class: ClassHiring, Type: "eng_hiring", Magnitude: 1.0, ObservedAt: base, Confidence: 0.7},
	})
	two := s.Score(ClassHiring, []Signal{
		{Class: ClassHiring, Type: "eng_hiring", Magnitude: 1.0, ObservedAt: base, Confidence: 0.7},
		{Class: ClassHiring, Type: "eng_hiring", Magnitude: 1.0, ObservedAt: base, Confidence: 0.7},
	})
	// eng_hiring weight 0.8 → one signal 0.8; two → 1-(0.2*0.2)=0.96.
	if !approx(one.Score, 0.8) || !approx(two.Score, 0.96) {
		t.Fatalf("one=%v two=%v, want 0.8 / 0.96", one.Score, two.Score)
	}
	if two.Score <= one.Score {
		t.Fatalf("corroboration should raise the score: one=%v two=%v", one.Score, two.Score)
	}
	if two.Confidence <= one.Confidence {
		t.Fatalf("corroboration should raise confidence: one=%v two=%v", one.Confidence, two.Confidence)
	}
}

func TestScore_NoEvidenceIsZero(t *testing.T) {
	s, base := fixedScorer()
	cs := s.Score(ClassBuying, []Signal{
		{Class: ClassHiring, Type: "job_posting", Magnitude: 1, ObservedAt: base}, // different class
	})
	if cs.Score != 0 || cs.Confidence != 0 || cs.SignalCount != 0 {
		t.Fatalf("no-evidence class = %+v, want zeroes", cs)
	}
}

type constCalibrator struct{ v float64 }

func (c constCalibrator) Calibrate(_ Class, _ float64) float64 { return c.v }

func TestScore_CalibratorHook(t *testing.T) {
	s, base := fixedScorer()
	s.Calibrator = constCalibrator{v: 0.99}
	cs := s.Score(ClassHiring, []Signal{{Class: ClassHiring, Type: "job_posting", Magnitude: 1, ObservedAt: base}})
	if !approx(cs.Score, 0.99) {
		t.Fatalf("calibrated score = %v, want 0.99", cs.Score)
	}
}

func TestScore_MagnitudeClamped(t *testing.T) {
	s, base := fixedScorer()
	// magnitude > 1 is clamped to 1 before weighting.
	cs := s.Score(ClassHiring, []Signal{{Class: ClassHiring, Type: "job_posting", Magnitude: 5.0, ObservedAt: base}})
	if !approx(cs.Score, 0.6) {
		t.Fatalf("clamped score = %v, want 0.6", cs.Score)
	}
}

func TestScoreAll_ScoresOnlyClassesWithSignals(t *testing.T) {
	s, base := fixedScorer()
	scores := s.ScoreAll([]Signal{
		{Class: ClassHiring, Type: "eng_hiring", Magnitude: 1, ObservedAt: base},
		{Class: ClassBuying, Type: "funding", Magnitude: 1, ObservedAt: base},
	})
	if len(scores) != 2 {
		t.Fatalf("scored %d classes, want 2", len(scores))
	}
	// Stable class order: buying precedes hiring in AllClasses().
	if scores[0].Class != ClassBuying || scores[1].Class != ClassHiring {
		t.Fatalf("class order = %v, %v", scores[0].Class, scores[1].Class)
	}
}

func TestScore_ReasoningSortedByWeightedDesc(t *testing.T) {
	s, base := fixedScorer()
	cs := s.Score(ClassHiring, []Signal{
		{Class: ClassHiring, Type: "sales_hiring", Magnitude: 1, ObservedAt: base}, // weight 0.5
		{Class: ClassHiring, Type: "eng_hiring", Magnitude: 1, ObservedAt: base},   // weight 0.8
	})
	if cs.Reasoning[0].Type != "eng_hiring" {
		t.Fatalf("reasoning not sorted by weighted desc: %+v", cs.Reasoning)
	}
}
