package workflows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/configver"
)

type fakeProviders map[string]configver.ProviderInfo

func (f fakeProviders) Lookup(_ context.Context, id string) (configver.ProviderInfo, bool, error) {
	info, ok := f[id]
	return info, ok, nil
}

type fakeBudgets map[string]int64

func (f fakeBudgets) Limit(_ context.Context, scope, scopeKey, period string) (int64, bool, error) {
	v, ok := f[scope+"|"+scopeKey+"|"+period]
	return v, ok, nil
}

func active(id string, caps ...string) configver.ProviderInfo {
	c := make([]configver.Capability, 0, len(caps))
	for _, f := range caps {
		c = append(c, configver.Capability{Field: f, Cost: 2, ExpectedConfidence: 0.8})
	}
	return configver.ProviderInfo{ID: id, Status: "ACTIVE-CANDIDATE", OpState: "enabled", Capabilities: c}
}

func codes(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var rep struct {
		Errors []configver.Finding `json:"errors"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("report not JSON: %v", err)
	}
	out := map[string]string{}
	for _, e := range rep.Errors {
		out[e.Code] = e.Message
	}
	return out
}

// TestWorkflowValidator_NegativeTable is the acceptance #5 table over the waterfall_workflow payload:
// EXCLUDED provider -> error, cycle -> error, max_cost_credits above the tenant budget -> error
// NAMING BOTH numbers, plus fallback==entry, empty stop_conditions, and out-of-range bounds — each
// with a stable code.
func TestWorkflowValidator_NegativeTable(t *testing.T) {
	provs := fakeProviders{
		"prospeo":  active("prospeo", "work_email"),
		"hunter":   active("hunter", "work_email"),
		"verifier": active("verifier", "email_status"),
		"clearbit": {ID: "clearbit", Status: "EXCLUDED", OpState: "enabled"},
	}
	budgets := fakeBudgets{"tenant||day": 1000}
	v := NewValidator(provs, budgets, func() time.Time { return time.Unix(1_800_000_000, 0) })

	base := func(overrides string) string {
		return `{"schema_version":1,"name":"wf","trigger":"api","fields":["work_email"],
			"entry_provider":"prospeo","timeout_ms":5000,"confidence_threshold":0.9,
			"max_cost_credits":50,"max_providers":4,"stop_conditions":["target-met"]` + overrides + `}`
	}

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"excluded provider", base(`,"sequential_providers":["clearbit"]`), "provider_excluded"},
		{"cycle entry repeats", base(`,"sequential_providers":["hunter","prospeo"],"fallback_provider":"verifier"`), "waterfall_cycle"},
		{"fallback equals entry", base(`,"fallback_provider":"prospeo"`), "fallback_equals_entry"},
		{"timeout out of bounds", `{"schema_version":1,"name":"w","trigger":"api","fields":["work_email"],"entry_provider":"prospeo","timeout_ms":10,"confidence_threshold":0.9,"max_cost_credits":10,"max_providers":2,"stop_conditions":["target-met"]}`, "timeout_out_of_bounds"},
		{"empty stop conditions", `{"schema_version":1,"name":"w","trigger":"api","fields":["work_email"],"entry_provider":"prospeo","timeout_ms":5000,"confidence_threshold":0.9,"max_cost_credits":10,"max_providers":2,"stop_conditions":[]}`, "stop_conditions_empty"},
		{"parallel too large", base(`,"parallel_providers":["hunter","verifier","clearbit","prospeo","hunter"]`), "parallel_group_too_large"},
		{"max_providers out of range", base(`,"max_providers":99`), "max_providers_out_of_bounds"},
		{"unknown field", `{"schema_version":1,"name":"w","trigger":"api","fields":["not_a_field"],"entry_provider":"prospeo","timeout_ms":5000,"confidence_threshold":0.9,"max_cost_credits":10,"max_providers":2,"stop_conditions":["target-met"]}`, "field_unknown"},
		{"gate override", `{"schema_version":1,"name":"w","trigger":"api","fields":["work_email"],"entry_provider":"prospeo","timeout_ms":5000,"confidence_threshold":0.9,"max_cost_credits":10,"max_providers":2,"stop_conditions":["target-met"],"bypass_g4":true}`, "gate_override_rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := v.Validate(context.Background(), configver.KindWaterfallWorkflow, "default", json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("internal fault: %v", err)
			}
			got := codes(t, report)
			if _, ok := got[tc.want]; !ok {
				t.Fatalf("want error code %q, got %v", tc.want, keys(got))
			}
		})
	}
}

// TestWorkflowValidator_BudgetNamesBothNumbers is the acceptance #5 budget case: max_cost_credits
// above the tenant budget errors, and the message names BOTH the config value and the budget.
func TestWorkflowValidator_BudgetNamesBothNumbers(t *testing.T) {
	provs := fakeProviders{"prospeo": active("prospeo", "work_email")}
	v := NewValidator(provs, fakeBudgets{"tenant||day": 1000}, time.Now)
	payload := `{"schema_version":1,"name":"w","trigger":"api","fields":["work_email"],
		"entry_provider":"prospeo","timeout_ms":5000,"confidence_threshold":0.9,
		"max_cost_credits":5000,"max_providers":4,"stop_conditions":["ceiling"]}`
	report, err := v.Validate(context.Background(), configver.KindWaterfallWorkflow, "default", json.RawMessage(payload))
	if err != nil {
		t.Fatalf("internal fault: %v", err)
	}
	got := codes(t, report)
	msg, ok := got["cost_exceeds_budget"]
	if !ok {
		t.Fatalf("expected cost_exceeds_budget, got %v", keys(got))
	}
	if !strings.Contains(msg, "5000") || !strings.Contains(msg, "1000") {
		t.Fatalf("VR-7 message must name both numbers (5000 and 1000): %q", msg)
	}
}

// TestWorkflowValidator_CleanPasses proves a valid workflow yields zero error findings.
func TestWorkflowValidator_CleanPasses(t *testing.T) {
	provs := fakeProviders{
		"prospeo":  active("prospeo", "work_email"),
		"hunter":   active("hunter", "work_email"),
		"verifier": active("verifier", "email_status"),
		"backup":   active("backup", "work_email"),
	}
	v := NewValidator(provs, fakeBudgets{"tenant||day": 100000}, time.Now)
	// A clean workflow names each Provider in exactly one phase (VR-5: a Provider revisited across
	// entry->parallel->sequential->fallback would form a cycle).
	payload := `{"schema_version":1,"name":"Email Finder","trigger":"api","fields":["work_email"],
		"entry_provider":"prospeo","parallel_providers":["hunter"],"sequential_providers":["verifier"],
		"timeout_ms":5000,"confidence_threshold":0.9,"min_score":0.5,"max_cost_credits":50,
		"max_providers":8,"fallback_provider":"backup","stop_conditions":["target-met","ceiling"]}`
	report, err := v.Validate(context.Background(), configver.KindWaterfallWorkflow, "default", json.RawMessage(payload))
	if err != nil {
		t.Fatalf("internal fault: %v", err)
	}
	if got := codes(t, report); len(got) != 0 {
		t.Fatalf("clean workflow produced errors: %v", keys(got))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
