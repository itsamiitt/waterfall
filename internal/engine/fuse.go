package engine

import (
	"math"

	"github.com/enrichment/waterfall/internal/domain"
)

// fuseAgreeing combines two independent confidences for the SAME value via log-odds
// (Bayesian) addition — the fusion step of the calibrate-then-fuse pipeline (ADR-0005).
// Two providers that independently report the same value reinforce each other, so the
// fused confidence exceeds either alone, while staying in [0,1).
//
// prior is the current fused confidence for a value (0 if first observation); obs is a
// new agreeing observation's confidence. Both are assumed already calibrated in [0,1].
func fuseAgreeing(prior, obs domain.Confidence) domain.Confidence {
	lp := logit(float64(prior.Clamp()))
	lo := logit(float64(obs.Clamp()))
	// Combine evidence in log-odds space. With no prior (prior==0 -> lp=-inf) the result
	// is just the observation, which is the desired "first observation" behaviour.
	switch {
	case math.IsInf(lp, -1):
		return obs.Clamp()
	case math.IsInf(lo, -1):
		return prior.Clamp()
	}
	return domain.Confidence(sigmoid(lp + lo)).Clamp()
}

// met reports whether fused confidence has reached the request's stop threshold — the
// sequential stop of the pipeline (SPRT-lite, ADR-0005): stop paying for a Field the
// moment it is "good enough".
func met(fused, target domain.Confidence) bool {
	if target <= 0 {
		return false
	}
	return fused >= target
}

func logit(p float64) float64 {
	switch {
	case p <= 0:
		return math.Inf(-1)
	case p >= 1:
		return math.Inf(1)
	default:
		return math.Log(p / (1 - p))
	}
}

func sigmoid(x float64) float64 {
	return 1 / (1 + math.Exp(-x))
}
