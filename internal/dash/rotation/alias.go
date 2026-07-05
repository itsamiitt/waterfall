package rotation

import "math/rand/v2"

// aliasTable is Vose's alias method: it turns a fixed weight distribution over n keys into two
// length-n tables (prob, alias) so a single draw is O(1) — one uniform index pick plus one
// coin flip (doc 07 §8 weighted: "alias-method table rebuilt on change; O(1) two-probe draw").
// It is immutable once built; a weight change rebuilds the whole PoolState, so the table is only
// read on the hot path (no locking needed).
type aliasTable struct {
	prob  []float64 // acceptance probability for column i
	alias []int     // fallback column i redirects to on rejection
	n     int
}

// newAliasTable builds the table from raw non-negative weights. Zero-length or all-zero weights
// yield a nil table (the caller falls back to a uniform walk). Weights need not be normalized.
func newAliasTable(weights []float64) *aliasTable {
	n := len(weights)
	if n == 0 {
		return nil
	}
	sum := 0.0
	for _, w := range weights {
		if w > 0 {
			sum += w
		}
	}
	if sum <= 0 {
		return nil
	}

	// Scaled probabilities: p_i * n, so the average column has value 1.
	scaled := make([]float64, n)
	for i, w := range weights {
		if w < 0 {
			w = 0
		}
		scaled[i] = w / sum * float64(n)
	}

	small := make([]int, 0, n)
	large := make([]int, 0, n)
	for i, s := range scaled {
		if s < 1 {
			small = append(small, i)
		} else {
			large = append(large, i)
		}
	}

	t := &aliasTable{prob: make([]float64, n), alias: make([]int, n), n: n}
	for len(small) > 0 && len(large) > 0 {
		l := small[len(small)-1]
		small = small[:len(small)-1]
		g := large[len(large)-1]
		large = large[:len(large)-1]

		t.prob[l] = scaled[l]
		t.alias[l] = g
		scaled[g] = scaled[g] + scaled[l] - 1
		if scaled[g] < 1 {
			small = append(small, g)
		} else {
			large = append(large, g)
		}
	}
	for _, g := range large {
		t.prob[g] = 1
	}
	for _, l := range small { // only from floating-point residue
		t.prob[l] = 1
	}
	return t
}

// sample draws one column index in [0,n) proportional to the original weights, O(1). rng is the
// caller's source (math/rand/v2 top-level funcs are used when nil is not passed by using a fresh
// Uint per call at the call site instead).
func (t *aliasTable) sample() int {
	col := rand.IntN(t.n)
	if rand.Float64() < t.prob[col] {
		return col
	}
	return t.alias[col]
}
