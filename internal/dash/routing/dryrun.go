package routing

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/router"
)

// DryRunner is the routing_policy Provider simulator (doc 07 §7): it runs the real router.Planner
// against the DRAFT payload with current reservation values and returns the by-Field Provider order
// + expected cost/Confidence — read-only, ZERO egress. It satisfies configver.DryRunner.
type DryRunner struct {
	providers configver.ProviderSource
	client    *http.Client  // wired into synthetic adapters but never used (zero-egress proof)
	scorer    router.Scorer // optional bandit posteriors
}

// NewDryRunner builds a routing DryRunner. client is optional (tests inject a RoundTripper that
// fails on any request to prove zero egress); scorer is optional (nil => static priors).
func NewDryRunner(providers configver.ProviderSource, client *http.Client, scorer router.Scorer) *DryRunner {
	return &DryRunner{providers: providers, client: client, scorer: scorer}
}

var _ configver.DryRunner = (*DryRunner)(nil)

// defaultWant is the Field set planned when the dry-run request names none.
var defaultWant = []domain.Field{domain.FieldWorkEmail}

// DryRun plans the draft routing policy. It gathers the candidate Providers (every id named in the
// waterfall ordering plus every override whose mode is not "off"), looks up their live
// capabilities, and runs the Planner. It returns resolved_scope provenance (this scope's overrides),
// the by_field order, and the worst-case committed spend.
func (d *DryRunner) DryRun(ctx context.Context, scopeKey string, payload json.RawMessage, req configver.DryRunRequest) (any, error) {
	var p Policy
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	want := parseWant(req.Want)

	candidates := map[string]bool{}
	off := map[string]bool{}
	for id, ov := range p.ProviderOverrides {
		if ov.Mode == "off" {
			off[id] = true
			continue
		}
		candidates[id] = true
	}
	for _, id := range p.Waterfall.Order {
		candidates[id] = true
	}
	if p.Waterfall.ParallelGroup != nil {
		for _, id := range p.Waterfall.ParallelGroup.Providers {
			candidates[id] = true
		}
	}
	for _, chain := range p.Waterfall.SequentialChains {
		for _, id := range chain {
			candidates[id] = true
		}
	}

	infos, err := lookupInfos(ctx, d.providers, candidates)
	if err != nil {
		return nil, err
	}
	plan := configver.PlanProviders(infos, want, off, d.client, d.scorer)

	overrides := map[string]any{}
	for id, ov := range p.ProviderOverrides {
		mode := ov.Mode
		if mode == "" {
			mode = "inherit"
		}
		overrides[id] = map[string]any{"effective": mode, "source": ScopeLevel(LevelTenantDefault).String()}
	}
	return map[string]any{
		"resolved_scope": map[string]any{
			"scope_key":        scopeKey,
			"levels_consulted": []string{"draft"},
			"overrides":        overrides,
		},
		"by_field":              plan.ByField,
		"max_committed_credits": plan.MaxCommittedCredits,
		"warnings":              []string{},
	}, nil
}

// lookupInfos resolves each candidate id to its ProviderInfo, silently dropping unknown ids (the
// validator is the gate for referencing an unknown Provider — the dry-run just plans what exists).
func lookupInfos(ctx context.Context, src configver.ProviderSource, ids map[string]bool) ([]configver.ProviderInfo, error) {
	out := make([]configver.ProviderInfo, 0, len(ids))
	for id := range ids {
		info, ok, err := src.Lookup(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, info)
		}
	}
	return out, nil
}

func parseWant(want []string) []domain.Field {
	if len(want) == 0 {
		return defaultWant
	}
	out := make([]domain.Field, 0, len(want))
	for _, f := range want {
		if df := domain.Field(f); df.Valid() {
			out = append(out, df)
		}
	}
	if len(out) == 0 {
		return defaultWant
	}
	return out
}
