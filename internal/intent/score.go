package intent

import (
	"math"
	"sort"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// Contribution is one signal's decayed, weighted contribution to a class score — the auditable "why"
// (G5). Every contribution (winner and loser) is retained on the ClassScore.
type Contribution struct {
	Type       string         `json:"type"`
	Provider   string         `json:"provider"`
	SourceType string         `json:"source_type"`
	Raw        float64        `json:"raw"`      // the signal's normalized magnitude
	Decayed    float64        `json:"decayed"`  // magnitude after freshness decay
	Weight     float64        `json:"weight"`   // the (class,type) weight applied
	Weighted   float64        `json:"weighted"` // decayed × weight, clamped [0,1]
	AgeHours   float64        `json:"age_hours"`
	Cost       domain.Credits `json:"cost"`
}

// ClassScore is the computed score for one intent class, with confidence and per-signal reasoning
// (ADR-0027). Distinct from the single-valued `intent_score` Field written back to the waterfall.
type ClassScore struct {
	Class       Class          `json:"class"`
	Score       float64        `json:"score"`      // combined evidence in [0,1] (calibrated if a Calibrator is set)
	Confidence  float64        `json:"confidence"` // corroboration confidence in [0,1]
	SignalCount int            `json:"signal_count"`
	Reasoning   []Contribution `json:"reasoning"`
}

// Calibrator maps a raw combined score to a calibrated probability per class (ADR-0005/0027). The
// production implementation wraps a per-class calibrate.IsotonicModel fitted by the offline-learning
// job; nil means identity (cold-start, UNVERIFIED).
type Calibrator interface {
	Calibrate(class Class, raw float64) float64
}

// Scorer computes class scores deterministically from signals, pinning a Weights config (ADR-0027).
type Scorer struct {
	Weights    Weights
	Calibrator Calibrator       // optional; nil ⇒ identity
	now        func() time.Time // injectable clock
}

// NewScorer builds a Scorer over a Weights config.
func NewScorer(w Weights) *Scorer { return &Scorer{Weights: w, now: time.Now} }

func (s *Scorer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Score computes the ClassScore for one class from its signals. Each signal is decayed by its
// freshness half-life, weighted by its (class,type) weight, and corroborated by a NOISY-OR combiner
// (0 with no evidence, monotonic in evidence). With a Calibrator set, the raw combined score is
// mapped to a calibrated probability. Every contribution is retained (G5), sorted by weighted desc.
func (s *Scorer) Score(class Class, signals []Signal) ClassScore {
	now := s.clock()
	cs := ClassScore{Class: class, Reasoning: []Contribution{}}
	oneMinusScore := 1.0
	oneMinusConf := 1.0
	for _, sig := range signals {
		if sig.Class != class {
			continue
		}
		age := now.Sub(sig.ObservedAt).Hours()
		if age < 0 {
			age = 0
		}
		decay := math.Pow(2, -age/s.Weights.halfLifeHours(sig.Type))
		decayed := clamp01(sig.Magnitude) * decay
		w := s.Weights.weight(class, sig.Type)
		weighted := clamp01(decayed * w)

		oneMinusScore *= 1 - weighted
		oneMinusConf *= 1 - clamp01(sig.Confidence)*decay

		cs.Reasoning = append(cs.Reasoning, Contribution{
			Type: sig.Type, Provider: sig.Provider, SourceType: sig.SourceType,
			Raw: sig.Magnitude, Decayed: decayed, Weight: w, Weighted: weighted, AgeHours: age, Cost: sig.Cost,
		})
		cs.SignalCount++
	}
	if cs.SignalCount == 0 {
		return cs // no evidence → Score 0, Confidence 0
	}
	raw := 1 - oneMinusScore
	if s.Calibrator != nil {
		raw = clamp01(s.Calibrator.Calibrate(class, raw))
	}
	cs.Score = raw
	cs.Confidence = 1 - oneMinusConf

	// Deterministic, readable reasoning: strongest contribution first, ties broken by type.
	sort.SliceStable(cs.Reasoning, func(i, j int) bool {
		if cs.Reasoning[i].Weighted != cs.Reasoning[j].Weighted {
			return cs.Reasoning[i].Weighted > cs.Reasoning[j].Weighted
		}
		return cs.Reasoning[i].Type < cs.Reasoning[j].Type
	})
	return cs
}

// ScoreAll scores every class that has at least one signal, in stable class order.
func (s *Scorer) ScoreAll(signals []Signal) []ClassScore {
	out := make([]ClassScore, 0, len(AllClasses()))
	for _, c := range AllClasses() {
		if cs := s.Score(c, signals); cs.SignalCount > 0 {
			out = append(out, cs)
		}
	}
	return out
}

func clamp01(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	default:
		return x
	}
}
