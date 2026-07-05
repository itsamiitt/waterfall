package workflows

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/router"
)

// DryRunner is the waterfall_workflow Provider simulator (doc 07 §7): the real router.Planner
// against the DRAFT payload with current reservation values, ZERO egress. Satisfies configver.DryRunner.
type DryRunner struct {
	providers configver.ProviderSource
	client    *http.Client
	scorer    router.Scorer
}

// NewDryRunner builds a workflow DryRunner. client is optional (tests inject a fail-on-request
// transport to prove zero egress); scorer is optional.
func NewDryRunner(providers configver.ProviderSource, client *http.Client, scorer router.Scorer) *DryRunner {
	return &DryRunner{providers: providers, client: client, scorer: scorer}
}

var _ configver.DryRunner = (*DryRunner)(nil)

// DryRun plans the draft workflow over its declared Fields (or a request override), returning the
// by_field Provider order + expected cost/Confidence, the worst-case committed spend, and a
// projected first-firing stop condition.
func (d *DryRunner) DryRun(ctx context.Context, scopeKey string, payload json.RawMessage, req configver.DryRunRequest) (any, error) {
	var w Workflow
	if err := json.Unmarshal(payload, &w); err != nil {
		return nil, err
	}
	want := d.planWant(w, req)

	candidates := map[string]bool{}
	for _, id := range append([]string{w.EntryProvider, w.FallbackProvider}, appendAll(w)...) {
		if id != "" {
			candidates[id] = true
		}
	}
	infos := make([]configver.ProviderInfo, 0, len(candidates))
	for id := range candidates {
		info, ok, err := d.providers.Lookup(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			infos = append(infos, info)
		}
	}
	plan := configver.PlanProviders(infos, want, nil, d.client, d.scorer)

	stop := "exhausted"
	if len(w.StopConditions) > 0 {
		stop = w.StopConditions[0]
	}
	used := 0
	for _, steps := range plan.ByField {
		if len(steps) > used {
			used = len(steps)
		}
	}
	return map[string]any{
		"resolved_scope":        map[string]any{"scope_key": scopeKey, "levels_consulted": []string{"draft"}},
		"by_field":              plan.ByField,
		"max_committed_credits": plan.MaxCommittedCredits,
		"stop_projection":       map[string]any{"condition": stop, "expected_providers_used": used},
		"warnings":              []string{},
	}, nil
}

func (d *DryRunner) planWant(w Workflow, req configver.DryRunRequest) []domain.Field {
	src := req.Want
	if len(src) == 0 {
		src = w.Fields
	}
	out := make([]domain.Field, 0, len(src))
	for _, f := range src {
		if df := domain.Field(f); df.Valid() {
			out = append(out, df)
		}
	}
	if len(out) == 0 {
		out = []domain.Field{domain.FieldWorkEmail}
	}
	return out
}

func appendAll(w Workflow) []string {
	out := make([]string, 0, len(w.ParallelProviders)+len(w.SequentialProviders))
	out = append(out, w.ParallelProviders...)
	out = append(out, w.SequentialProviders...)
	return out
}
