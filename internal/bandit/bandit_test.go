package bandit

import (
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
)

const f = domain.FieldWorkEmail

func TestUpdateShiftsMean(t *testing.T) {
	b := New()
	if m := b.Mean("p", f); m != 0.5 {
		t.Fatalf("uniform prior mean should be 0.5, got %.3f", m)
	}
	for i := 0; i < 8; i++ {
		b.Update("good", f, true)
	}
	for i := 0; i < 8; i++ {
		b.Update("bad", f, false)
	}
	if b.Mean("good", f) <= 0.7 {
		t.Fatalf("good posterior mean should rise, got %.3f", b.Mean("good", f))
	}
	if b.Mean("bad", f) >= 0.3 {
		t.Fatalf("bad posterior mean should fall, got %.3f", b.Mean("bad", f))
	}
}

func TestConservativeFloor_NoDataUsesPrior(t *testing.T) {
	b := New()
	s := b.NewScorer(1)
	// With zero observations, the score must equal the static prior (blend weight 0).
	if got := s.Score("p", f, 0.42); got != 0.42 {
		t.Fatalf("no-data score should equal the prior 0.42, got %.3f", got)
	}
}

func TestScorer_Reproducible(t *testing.T) {
	b := New()
	for i := 0; i < 10; i++ {
		b.Update("p", f, i%2 == 0)
	}
	s1 := b.NewScorer(99)
	s2 := b.NewScorer(99)
	for i := 0; i < 5; i++ {
		if s1.Score("p", f, 0.5) != s2.Score("p", f, 0.5) {
			t.Fatal("same seed + same posteriors must produce identical scores (replayable routing)")
		}
	}
}

func TestScorer_SampleInRange(t *testing.T) {
	b := New()
	for i := 0; i < 20; i++ {
		b.Update("p", f, true) // strong success -> mean near 0.95
	}
	s := b.NewScorer(7)
	sum := 0.0
	const n = 200
	for i := 0; i < n; i++ {
		v := s.Score("p", f, 0.5)
		if v < 0 || v > 1 {
			t.Fatalf("score out of range: %.3f", v)
		}
		sum += v
	}
	avg := sum / n
	if avg < b.Mean("p", f)-0.1 || avg > b.Mean("p", f)+0.1 {
		t.Fatalf("sample average %.3f should track the posterior mean %.3f", avg, b.Mean("p", f))
	}
}
