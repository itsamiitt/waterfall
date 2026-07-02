// Package bandit is a Thompson-sampling router scorer (ADR-0008): it learns, per
// (provider, field), a Beta posterior over "did this provider yield the field?" and, when
// asked to score a provider for ordering, samples that posterior. The router uses the
// sample to order the cascade — but ONLY to order. The Execution Engine still re-enforces
// G3/G4 before every call, so the bandit can influence WHICH provider is tried first, never
// whether a bound or the cost ceiling is respected ("model proposes, deterministic gate
// disposes", docs/04).
//
// Conservative floor (ADR-0008): a provider with few observations is scored as a blend of
// its sampled posterior and the static prior, so a barely-observed provider is neither
// blindly trusted nor unfairly buried until enough evidence accrues.
package bandit

import (
	"math"
	"math/rand"
	"sync"

	"github.com/enrichment/waterfall/internal/domain"
)

type key struct {
	provider string
	field    domain.Field
}

type stat struct{ succ, fail float64 }

// Bandit holds the learned posteriors. It is safe for concurrent Update/read.
type Bandit struct {
	mu         sync.Mutex
	stats      map[key]*stat
	blendPulls float64 // conservative floor: pulls needed before fully trusting the posterior
}

// New builds an empty bandit (uniform Beta(1,1) prior for every provider/field).
func New() *Bandit {
	return &Bandit{stats: map[key]*stat{}, blendPulls: 5}
}

// Update records whether a provider yielded the field on a real call.
func (b *Bandit) Update(provider string, field domain.Field, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := key{provider, field}
	s := b.stats[k]
	if s == nil {
		s = &stat{}
		b.stats[k] = s
	}
	if success {
		s.succ++
	} else {
		s.fail++
	}
}

// params returns the Beta parameters (alpha, beta) = (1+successes, 1+failures).
func (b *Bandit) params(provider string, field domain.Field) (float64, float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats[key{provider, field}]
	if s == nil {
		return 1, 1
	}
	return 1 + s.succ, 1 + s.fail
}

// Mean is the posterior mean success probability for a (provider, field).
func (b *Bandit) Mean(provider string, field domain.Field) float64 {
	a, bb := b.params(provider, field)
	return a / (a + bb)
}

// Scorer is a per-plan sampler with its own RNG, so a plan's ordering is reproducible from
// its seed (docs/04 §4: seed the Thompson RNG from the record so routing replays). It
// implements router.Scorer structurally (no import cycle).
type Scorer struct {
	b   *Bandit
	rng *rand.Rand
}

// NewScorer returns a scorer seeded deterministically. Same seed + same posteriors => same
// ordering.
func (b *Bandit) NewScorer(seed int64) *Scorer {
	return &Scorer{b: b, rng: rand.New(rand.NewSource(seed))}
}

// Score samples the provider's posterior and blends it with the static prior according to
// how much evidence exists (the conservative floor).
func (s *Scorer) Score(provider string, field domain.Field, prior domain.Confidence) float64 {
	a, bb := s.b.params(provider, field)
	sample := betaSample(s.rng, a, bb)
	pulls := (a - 1) + (bb - 1)
	w := pulls / (pulls + s.b.blendPulls) // 0 with no data -> 1 with plenty
	return w*sample + (1-w)*float64(prior)
}

// betaSample draws from Beta(a,b) via two Gamma draws (a,b >= 1 in our use).
func betaSample(rng *rand.Rand, a, b float64) float64 {
	ga := gammaSample(rng, a)
	gb := gammaSample(rng, b)
	if ga+gb == 0 {
		return 0.5
	}
	return ga / (ga + gb)
}

// gammaSample draws from Gamma(shape, 1) for shape >= 1 (Marsaglia-Tsang).
func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		shape = 1
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}
