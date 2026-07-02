// Package router turns an EnrichmentRequest into an ordered Plan of provider calls.
//
// It embodies the governing invariant "model proposes, deterministic gate disposes"
// (docs/04): the router PROPOSES an order (here a deterministic reservation-value
// ordering — a stand-in for the Thompson-sampling router of ADR-0008), but it never
// makes a provider call and never enforces the cost ceiling itself. The Execution
// Engine re-checks G3/G4 before every call. So a bug in routing can waste ordering, but
// it can never overspend or bypass a bound.
package router

import (
	"sort"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Step is one proposed provider call for one Field.
type Step struct {
	Field              domain.Field
	Provider           string
	Cost               domain.Credits
	ExpectedConfidence domain.Confidence
}

// Plan is the per-Field ordered proposal. Steps for a Field are tried in slice order
// until the confidence target is met or the budget is exhausted (decided by the engine).
type Plan struct {
	ByField map[domain.Field][]Step
	Order   []domain.Field // stable field processing order
}

// Scorer estimates a provider's ordering merit for a field, given its static prior
// confidence. The default (nil) uses the static prior; a bandit (ADR-0008) returns a
// Thompson-sampled estimate. The scorer only influences ORDER — the engine still enforces
// G3/G4, so a scorer can never cause overspend or skip a bound.
type Scorer interface {
	Score(provider string, field domain.Field, prior domain.Confidence) float64
}

// Planner builds Plans from the set of available adapters.
type Planner struct {
	adapters []provider.Adapter
	scorer   Scorer
}

// New builds a Planner over the given adapters.
func New(adapters ...provider.Adapter) *Planner {
	return &Planner{adapters: adapters}
}

// WithScorer returns a Planner that orders providers by scorer (e.g. a bandit) instead of
// the static prior. It returns the same Planner for chaining.
func (p *Planner) WithScorer(s Scorer) *Planner {
	p.scorer = s
	return p
}

// Plan produces an ordered proposal for req. For each wanted Field it gathers every
// adapter that advertises the capability and orders them by reservation value — expected
// confidence per credit — highest first, so the cheapest high-yield provider is tried
// before falling through to costlier or weaker ones (Pandora/Weitzman-style cascade,
// ADR-0007). Ordering is fully deterministic (ties broken by cost then name) so plans
// are reproducible and diffable.
func (p *Planner) Plan(req domain.EnrichmentRequest) Plan {
	plan := Plan{ByField: map[domain.Field][]Step{}}
	for _, f := range req.Want {
		if !f.Valid() {
			continue // never plan a call for a non-canonical field
		}
		var steps []Step
		for _, a := range p.adapters {
			if cap, ok := provider.Can(a, f); ok {
				steps = append(steps, Step{
					Field:              f,
					Provider:           a.Name(),
					Cost:               cap.Cost,
					ExpectedConfidence: cap.ExpectedConfidence,
				})
			}
		}
		if len(steps) == 0 {
			continue
		}
		// Score each provider ONCE (a bandit scorer samples, so it must not be re-evaluated
		// inside the sort comparator), then order by score-per-credit.
		score := make(map[string]float64, len(steps))
		for _, s := range steps {
			est := float64(s.ExpectedConfidence)
			if p.scorer != nil {
				est = p.scorer.Score(s.Provider, f, s.ExpectedConfidence)
			}
			score[s.Provider] = est
		}
		sort.SliceStable(steps, func(i, j int) bool {
			di, dj := density(steps[i], score[steps[i].Provider]), density(steps[j], score[steps[j].Provider])
			if di != dj {
				return di > dj // higher score-per-credit first
			}
			if steps[i].Cost != steps[j].Cost {
				return steps[i].Cost < steps[j].Cost // cheaper first
			}
			return steps[i].Provider < steps[j].Provider // stable, deterministic
		})
		plan.ByField[f] = steps
		plan.Order = append(plan.Order, f)
	}
	return plan
}

// density is estimated score per credit; a zero-cost provider is ranked by its raw score
// (treated as infinitely dense but ordered among themselves by score).
func density(s Step, est float64) float64 {
	if s.Cost <= 0 {
		return est * 1e6
	}
	return est / float64(s.Cost)
}

// MaxCommitted returns the worst-case committed spend if every planned step ran. The
// engine will not actually let spend exceed the ceiling; this is a planning-time
// estimate for surfacing "this record cannot be fully satisfied within budget".
func (pl Plan) MaxCommitted() domain.Credits {
	var total domain.Credits
	for _, steps := range pl.ByField {
		for _, s := range steps {
			total += s.Cost
		}
	}
	return total
}
