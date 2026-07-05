package configver

import (
	"context"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/router"
)

// PlanStep is one planned Provider call for one Field in a dry-run projection.
type PlanStep struct {
	Provider           string  `json:"provider"`
	CostCredits        int64   `json:"cost_credits"`
	ExpectedConfidence float64 `json:"expected_confidence"`
}

// PlanResult is the by-Field planned order + worst-case spend a dry-run returns (doc 07 §7). The
// per-kind DryRunner wraps it with resolved_scope / stop_projection provenance.
type PlanResult struct {
	ByField             map[string][]PlanStep `json:"by_field"`
	MaxCommittedCredits int64                 `json:"max_committed_credits"`
}

// planAdapter is a synthetic provider.Adapter carrying only declared Capabilities. Its Fetch would
// egress through client — but the router.Planner is a PURE function over Capabilities() and Name()
// and NEVER calls Fetch, which is exactly what the dry-run zero-egress test asserts (a client whose
// RoundTripper fails the test is wired in yet never hit).
type planAdapter struct {
	name   string
	caps   []provider.Capability
	client *http.Client
}

func (a planAdapter) Name() string                        { return a.name }
func (a planAdapter) Capabilities() []provider.Capability { return a.caps }

func (a planAdapter) Fetch(ctx context.Context, req provider.Request) (provider.Result, error) {
	// Never reached on the dry-run path. If it ever were, it would perform live egress through
	// client — so the zero-egress test's failing RoundTripper would trip. Read-only simulator
	// (doc 07 §7): zero Provider calls, no paid credits.
	if a.client != nil {
		_, _ = a.client.Get("http://waterfall.simulated.invalid/should-never-be-called")
	}
	return provider.Result{}, nil
}

var _ provider.Adapter = planAdapter{}

// PlanProviders builds a router.Planner over adapters synthesized from infos (skipping any id in
// off), plans the wanted Fields with current reservation values (an optional Scorer supplies bandit
// posteriors), and returns the by-Field order + worst-case spend. It makes ZERO egress: the Planner
// only reads Capabilities(). client, when non-nil, is wired into the adapters solely so the
// zero-egress test can prove Fetch is never invoked.
func PlanProviders(infos []ProviderInfo, want []domain.Field, off map[string]bool, client *http.Client, scorer router.Scorer) PlanResult {
	adapters := make([]provider.Adapter, 0, len(infos))
	for _, in := range infos {
		if off[in.ID] {
			continue
		}
		caps := make([]provider.Capability, 0, len(in.Capabilities))
		for _, c := range in.Capabilities {
			caps = append(caps, provider.Capability{
				Field:              domain.Field(c.Field),
				Cost:               domain.Credits(c.Cost),
				ExpectedConfidence: domain.Confidence(c.ExpectedConfidence),
			})
		}
		adapters = append(adapters, planAdapter{name: in.ID, caps: caps, client: client})
	}
	pl := router.New(adapters...)
	if scorer != nil {
		pl = pl.WithScorer(scorer)
	}
	plan := pl.Plan(domain.EnrichmentRequest{Want: want})

	out := PlanResult{ByField: map[string][]PlanStep{}}
	for field, steps := range plan.ByField {
		row := make([]PlanStep, 0, len(steps))
		for _, s := range steps {
			row = append(row, PlanStep{
				Provider:           s.Provider,
				CostCredits:        int64(s.Cost),
				ExpectedConfidence: float64(s.ExpectedConfidence),
			})
		}
		out.ByField[string(field)] = row
	}
	out.MaxCommittedCredits = int64(plan.MaxCommitted())
	return out
}
