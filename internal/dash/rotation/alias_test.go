package rotation

import (
	"math"
	"testing"
)

// TestAliasDistribution is P2 acceptance #3 (doc 12): 1,000,000 draws over weights {70,20,10} must
// land within +/-1% absolute of the expected {0.70, 0.20, 0.10}. The alias table is the O(1)
// weighted-selection mechanism (doc 07 §8).
func TestAliasDistribution(t *testing.T) {
	weights := []float64{70, 20, 10}
	at := newAliasTable(weights)
	if at == nil {
		t.Fatal("newAliasTable returned nil for valid weights")
	}

	const draws = 1_000_000
	counts := make([]int, len(weights))
	for i := 0; i < draws; i++ {
		counts[at.sample()]++
	}

	expected := []float64{0.70, 0.20, 0.10}
	for i, want := range expected {
		got := float64(counts[i]) / float64(draws)
		if diff := math.Abs(got - want); diff > 0.01 {
			t.Fatalf("weight[%d]: got proportion %.4f, want %.2f (|diff|=%.4f > 0.01)", i, got, want, diff)
		}
	}
	t.Logf("PASS alias distribution over {70,20,10}: %.4f / %.4f / %.4f (1e6 draws, within +/-1%%)",
		float64(counts[0])/draws, float64(counts[1])/draws, float64(counts[2])/draws)
}

// TestAliasEdgeCases covers degenerate weight sets.
func TestAliasEdgeCases(t *testing.T) {
	if newAliasTable(nil) != nil {
		t.Fatal("nil weights should yield a nil table")
	}
	if newAliasTable([]float64{0, 0, 0}) != nil {
		t.Fatal("all-zero weights should yield a nil table")
	}
	at := newAliasTable([]float64{1})
	if at == nil || at.sample() != 0 {
		t.Fatal("single-weight table must always sample index 0")
	}
}
