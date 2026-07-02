// Package calibrate turns a provider's raw self-reported score into a calibrated
// probability that a field value is correct (ADR-0005, docs/15). It implements isotonic
// regression via the Pool-Adjacent-Violators Algorithm (PAVA): a monotonic, non-parametric
// fit that maps raw score -> P(correct) without assuming a functional form.
//
// Calibration is config-as-data (docs/06): models are FITTED OFFLINE from labeled outcomes
// and applied deterministically at enrichment time — the engine never trains in the hot
// path. This keeps the governing invariant intact: a learned mapping proposes a number, the
// deterministic pipeline (fuse -> stop -> merge) disposes.
package calibrate

import (
	"sort"

	"github.com/enrichment/waterfall/internal/domain"
)

// Point is one labeled observation: the provider reported score X, and the value turned out
// correct (Y=1) or not (Y=0). W is an optional weight (default 1).
type Point struct {
	X, Y, W float64
}

// IsotonicModel is a fitted monotonic step function score -> probability.
type IsotonicModel struct {
	xs []float64 // block start scores, non-decreasing
	ys []float64 // block probabilities, non-decreasing
}

// FitIsotonic fits a non-decreasing calibration curve to pts via PAVA.
func FitIsotonic(pts []Point) *IsotonicModel {
	if len(pts) == 0 {
		return &IsotonicModel{}
	}
	ps := append([]Point(nil), pts...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].X < ps[j].X })

	type block struct{ x, val, w float64 }
	var blocks []block
	for _, p := range ps {
		w := p.W
		if w <= 0 {
			w = 1
		}
		blocks = append(blocks, block{x: p.X, val: p.Y, w: w})
		// Pool adjacent violators: while the last block's value is below its predecessor's,
		// merge them into their weighted mean (restores monotonicity).
		for len(blocks) >= 2 && blocks[len(blocks)-2].val > blocks[len(blocks)-1].val {
			b2 := blocks[len(blocks)-1]
			b1 := blocks[len(blocks)-2]
			merged := block{
				x:   b1.x, // keep the left (min) score of the pooled block
				val: (b1.val*b1.w + b2.val*b2.w) / (b1.w + b2.w),
				w:   b1.w + b2.w,
			}
			blocks = blocks[:len(blocks)-2]
			blocks = append(blocks, merged)
		}
	}
	m := &IsotonicModel{}
	for _, b := range blocks {
		m.xs = append(m.xs, b.x)
		m.ys = append(m.ys, clamp01(b.val))
	}
	return m
}

// Predict returns the calibrated probability for a raw score. An empty model is the
// identity (returns the input), so an uncalibrated (provider,field) is left unchanged.
func (m *IsotonicModel) Predict(x float64) float64 {
	if len(m.xs) == 0 {
		return clamp01(x)
	}
	if x <= m.xs[0] {
		return m.ys[0]
	}
	// rightmost block whose start score <= x
	i := sort.Search(len(m.xs), func(i int) bool { return m.xs[i] > x }) - 1
	if i < 0 {
		i = 0
	}
	return m.ys[i]
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// --- Calibrator: per-(provider,field) models ---

type key struct {
	provider string
	field    domain.Field
}

// Calibrator holds fitted models per (provider, field). It is safe for concurrent reads
// after fitting (fitting is an offline/config-load step, not concurrent with enrichment).
type Calibrator struct {
	models map[key]*IsotonicModel
}

// New builds an empty calibrator (identity for every provider/field until fitted).
func New() *Calibrator {
	return &Calibrator{models: map[key]*IsotonicModel{}}
}

// Fit trains and installs the model for one (provider, field).
func (c *Calibrator) Fit(provider string, field domain.Field, pts []Point) {
	c.models[key{provider, field}] = FitIsotonic(pts)
}

// Calibrate maps a raw provider confidence to a calibrated one. Unknown (provider,field)
// pairs pass through unchanged (identity), so calibration is strictly opt-in per pair.
func (c *Calibrator) Calibrate(provider string, field domain.Field, raw domain.Confidence) domain.Confidence {
	if c == nil {
		return raw
	}
	m, ok := c.models[key{provider, field}]
	if !ok {
		return raw
	}
	return domain.Confidence(m.Predict(float64(raw))).Clamp()
}
