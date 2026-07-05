package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/configver"
)

// Validator runs the waterfall_workflow VR catalog (doc 07 §5) over a payload. It satisfies
// configver.Validator; the shared lifecycle engine invokes it at validate and re-invokes it at
// rollback (OI-RW-3 world-drift protection).
type Validator struct {
	providers configver.ProviderSource
	budgets   configver.BudgetSource
	now       func() time.Time
}

// NewValidator builds a workflow Validator over the Provider + budget lookups.
func NewValidator(providers configver.ProviderSource, budgets configver.BudgetSource, now func() time.Time) *Validator {
	if now == nil {
		now = time.Now
	}
	return &Validator{providers: providers, budgets: budgets, now: now}
}

var _ configver.Validator = (*Validator)(nil)

const (
	timeoutMin   = 250
	timeoutMax   = 120000
	parallelMax  = 4 // VR-6 PARALLEL_GROUP_MAX (ADR-0007)
	providersMin = 1 // VR-16
	providersMax = 16
)

// Validate runs every rule and returns the report ({"errors","warnings"}). A failed rule is report
// content; a non-nil error is an internal fault.
func (v *Validator) Validate(ctx context.Context, kind, scopeKey string, payload json.RawMessage) (json.RawMessage, error) {
	var w Workflow
	if err := json.Unmarshal(payload, &w); err != nil {
		return structuralError("the payload is not a valid waterfall_workflow object"), nil
	}
	ck := configver.NewChecker(ctx, v.providers, v.budgets, v.now())

	// VR-16 catch-all: reject any attempt to override the G3/G4 engine gates.
	ck.GateOverride(payload)

	// Trigger vocabulary (doc 07 §4).
	if w.Trigger != "" && !triggerSet[w.Trigger] {
		ck.Error("VR-11", "trigger_unknown", "/trigger", fmt.Sprintf("trigger %q is not one of api|batch|webhook", w.Trigger))
	}

	// VR-15: canonical target Fields.
	ck.FieldVocabulary("/fields", w.Fields)

	// VR-1/2/3/4/12: every referenced Provider.
	entry, _ := ck.Provider("/entry_provider", w.EntryProvider, "")
	for i, id := range w.ParallelProviders {
		ck.Provider(pathIdx("/parallel_providers", i), id, "")
	}
	for i, id := range w.SequentialProviders {
		ck.Provider(pathIdx("/sequential_providers", i), id, "")
	}
	if w.FallbackProvider != "" {
		ck.Provider("/fallback_provider", w.FallbackProvider, "")
	}

	// VR-15 (warning): the entry Provider should advertise a capability for at least one Field.
	if entry.ID != "" && len(w.Fields) > 0 {
		if !anyCapability(entry, w.Fields) {
			ck.Warn("VR-15", "provider_no_capability", "/entry_provider",
				fmt.Sprintf("entry provider %s advertises no capability for any requested field", entry.ID))
		}
	}

	// VR-6: parallel group size bound.
	if len(w.ParallelProviders) > parallelMax {
		ck.Error("VR-6", "parallel_group_too_large", "/parallel_providers",
			fmt.Sprintf("parallel group has %d providers, exceeding the bounded cheap-prefix cap of %d",
				len(w.ParallelProviders), parallelMax))
	}

	// VR-16: no duplicate Provider within a single ordering list.
	ck.Duplicate("/parallel_providers", w.ParallelProviders)
	ck.Duplicate("/sequential_providers", w.SequentialProviders)

	// VR-5: acyclicity across entry -> parallel -> sequential -> fallback.
	spine := buildSpine(w)
	ck.Acyclic("/", spine)

	// VR-8: Confidence-typed values in [0,1].
	ck.Confidence("/confidence_threshold", w.ConfidenceThreshold)
	ck.Confidence("/min_score", w.MinScore)

	// VR-9: timeout within engine bounds.
	if w.TimeoutMS != nil && (*w.TimeoutMS < timeoutMin || *w.TimeoutMS > timeoutMax) {
		ck.Error("VR-9", "timeout_out_of_bounds", "/timeout_ms",
			fmt.Sprintf("timeout_ms %d is outside the engine bounds [%d, %d]", *w.TimeoutMS, timeoutMin, timeoutMax))
	}

	// VR-10: fallback_provider must differ from entry_provider.
	if w.FallbackProvider != "" && w.FallbackProvider == w.EntryProvider {
		ck.Error("VR-10", "fallback_equals_entry", "/fallback_provider",
			fmt.Sprintf("fallback_provider %s must differ from entry_provider", w.FallbackProvider))
	}

	// VR-11: stop_conditions non-empty and a subset of the closed enum.
	if len(w.StopConditions) == 0 {
		ck.Error("VR-11", "stop_conditions_empty", "/stop_conditions", "stop_conditions must be non-empty")
	}
	for i, sc := range w.StopConditions {
		if !stopConditionSet[sc] {
			ck.Error("VR-11", "stop_condition_unknown", pathIdx("/stop_conditions", i),
				fmt.Sprintf("stop condition %q is not one of target-met|ceiling|exhausted|timeout", sc))
		}
	}

	// VR-16: max_providers within [1,16].
	if w.MaxProviders != nil && (*w.MaxProviders < providersMin || *w.MaxProviders > providersMax) {
		ck.Error("VR-16", "max_providers_out_of_bounds", "/max_providers",
			fmt.Sprintf("max_providers %d is outside the allowed range [%d, %d]", *w.MaxProviders, providersMin, providersMax))
	}

	// VR-7: per-Job spend must not exceed the Tenant budget.
	ck.MaxCost("/max_cost_credits", w.MaxCostCredits, "tenant", "", "day")
	ck.MaxCost("/max_cost_credits", w.MaxCostCredits, "tenant", "", "month")

	if err := ck.Fault(); err != nil {
		return nil, err
	}
	return ck.Report(), nil
}

// buildSpine flattens the phase order entry -> parallel -> sequential -> fallback into one directed
// list so a Provider repeated across phases surfaces as a VR-5 cycle.
func buildSpine(w Workflow) []string {
	var spine []string
	if w.EntryProvider != "" {
		spine = append(spine, w.EntryProvider)
	}
	spine = append(spine, w.ParallelProviders...)
	spine = append(spine, w.SequentialProviders...)
	if w.FallbackProvider != "" {
		spine = append(spine, w.FallbackProvider)
	}
	return spine
}

func anyCapability(info configver.ProviderInfo, fields []string) bool {
	for _, f := range fields {
		if info.HasCapability(f) {
			return true
		}
	}
	return false
}

func structuralError(msg string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"errors": []configver.Finding{{
			Rule: "VR-0", Code: "payload_malformed", Severity: configver.SevError, Path: "/", Message: msg,
		}},
		"warnings": []configver.Finding{},
	})
	return b
}

func pathIdx(base string, i int) string { return base + "/" + strconv.Itoa(i) }
