package routing

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/configver"
)

// Validator runs the routing_policy VR catalog (doc 07 §5) over a payload. It satisfies
// configver.Validator, so the shared lifecycle engine invokes it at POST .../validate and
// re-invokes it at rollback (OI-RW-3 world-drift protection).
type Validator struct {
	providers configver.ProviderSource
	budgets   configver.BudgetSource
	now       func() time.Time
}

// NewValidator builds a routing Validator over the Provider + budget lookups.
func NewValidator(providers configver.ProviderSource, budgets configver.BudgetSource, now func() time.Time) *Validator {
	if now == nil {
		now = time.Now
	}
	return &Validator{providers: providers, budgets: budgets, now: now}
}

var _ configver.Validator = (*Validator)(nil)

// Validate runs every rule and returns the machine-readable report ({"errors","warnings"}). A
// failed rule is report content, never a transport error (doc 07 §5); a non-nil error signals an
// internal fault (e.g. a Provider lookup failed).
func (v *Validator) Validate(ctx context.Context, kind, scopeKey string, payload json.RawMessage) (json.RawMessage, error) {
	var p Policy
	if err := json.Unmarshal(payload, &p); err != nil {
		// A payload that is not shaped like a routing_policy is a single structural error.
		ck := configver.NewChecker(ctx, v.providers, v.budgets, v.now())
		return structuralError(ck, "the payload is not a valid routing_policy object"), nil
	}
	ck := configver.NewChecker(ctx, v.providers, v.budgets, v.now())

	// VR-16 catch-all: reject any attempt to override the G3/G4 engine gates.
	ck.GateOverride(payload)

	// VR-14: the scope echo must agree with the row scope_key dimensions.
	checkScopeEcho(ck, p.Scope, scopeKey)

	// VR-1/2/3/4/12: every referenced Provider. Overrides carry the tri-state mode.
	for _, id := range sortedKeys(p.ProviderOverrides) {
		ck.Provider("/provider_overrides/"+id, id, p.ProviderOverrides[id].Mode)
	}
	for i, id := range p.Waterfall.Order {
		ck.Provider(pathIdx("/waterfall/order", i), id, "")
	}
	if p.Waterfall.ParallelGroup != nil {
		for i, id := range p.Waterfall.ParallelGroup.Providers {
			ck.Provider(pathIdx("/waterfall/parallel_group/providers", i), id, "")
		}
	}
	for ci, chain := range p.Waterfall.SequentialChains {
		for i, id := range chain {
			ck.Provider(pathIdx2("/waterfall/sequential_chains", ci, i), id, "")
		}
	}

	// VR-16: no duplicate Provider within a single ordering list.
	ck.Duplicate("/waterfall/order", p.Waterfall.Order)
	ck.Duplicate("/waterfall/retry_order", p.Waterfall.RetryOrder)
	ck.Duplicate("/waterfall/failover_order", p.Waterfall.FailoverOrder)

	// VR-5: acyclicity across all ordering constructs.
	edgeLists := [][]string{p.Waterfall.Order, p.Waterfall.RetryOrder, p.Waterfall.FailoverOrder}
	edgeLists = append(edgeLists, p.Waterfall.SequentialChains...)
	ck.Acyclic("/waterfall", edgeLists...)

	// VR-8: Confidence thresholds in [0,1].
	ck.Confidence("/thresholds/confidence_target", p.Thresholds.ConfidenceTarget)
	ck.Confidence("/thresholds/min_confidence", p.Thresholds.MinConfidence)

	// VR-7: per-record / per-field cost must not exceed the Tenant budget.
	ck.MaxCost("/thresholds/max_cost_credits_per_record", p.Thresholds.MaxCostCreditsPerRecord, "tenant", "", "day")
	ck.MaxCost("/thresholds/max_cost_credits_per_record", p.Thresholds.MaxCostCreditsPerRecord, "tenant", "", "month")

	if err := ck.Fault(); err != nil {
		return nil, err
	}
	return ck.Report(), nil
}

// --- shared validator helpers (also used by workflows) ---

func structuralError(_ *configver.Checker, msg string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"errors": []configver.Finding{{
			Rule: "VR-0", Code: "payload_malformed", Severity: configver.SevError, Path: "/", Message: msg,
		}},
		"warnings": []configver.Finding{},
	})
	return b
}

func checkScopeEcho(ck *configver.Checker, s Scope, scopeKey string) {
	dims, ok := configver.ParseScopeKey(scopeKey)
	if !ok {
		return // invalid scope_key is caught at draft creation; nothing to echo against
	}
	if s.Product != "" && s.Product != dims.Product {
		ck.Error("VR-14", "scope_mismatch", "/scope/product", "product echo does not match the row scope_key")
	}
	if s.Country != "" && s.Country != dims.Country {
		ck.Error("VR-14", "scope_mismatch", "/scope/country", "country echo does not match the row scope_key")
	}
}

func sortedKeys(m map[string]Override) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pathIdx(base string, i int) string { return base + "/" + strconv.Itoa(i) }
func pathIdx2(base string, a, b int) string {
	return base + "/" + strconv.Itoa(a) + "/" + strconv.Itoa(b)
}
