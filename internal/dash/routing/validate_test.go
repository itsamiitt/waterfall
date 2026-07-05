package routing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/configver"
)

// --- test fakes for the ProviderSource + BudgetSource seams ---

type fakeProviders map[string]configver.ProviderInfo

func (f fakeProviders) Lookup(_ context.Context, id string) (configver.ProviderInfo, bool, error) {
	info, ok := f[id]
	return info, ok, nil
}

type fakeBudgets map[string]int64 // key: scope|scope_key|period

func (f fakeBudgets) Limit(_ context.Context, scope, scopeKey, period string) (int64, bool, error) {
	v, ok := f[scope+"|"+scopeKey+"|"+period]
	return v, ok, nil
}

// reportCodes extracts the error/warning codes from a validator report for table assertions.
func reportCodes(t *testing.T, raw json.RawMessage) (errs, warns map[string]bool) {
	t.Helper()
	var rep struct {
		Errors   []configver.Finding `json:"errors"`
		Warnings []configver.Finding `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("report is not valid JSON: %v (%s)", err, raw)
	}
	errs, warns = map[string]bool{}, map[string]bool{}
	for _, e := range rep.Errors {
		errs[e.Code] = true
	}
	for _, w := range rep.Warnings {
		warns[w.Code] = true
	}
	return errs, warns
}

func active(field string, caps ...string) configver.ProviderInfo {
	c := make([]configver.Capability, 0, len(caps))
	for _, f := range caps {
		c = append(c, configver.Capability{Field: f, Cost: 2, ExpectedConfidence: 0.8})
	}
	return configver.ProviderInfo{ID: field, Status: "ACTIVE-CANDIDATE", OpState: "enabled", Capabilities: c}
}

// TestRoutingValidator_NegativeTable is the acceptance #5 routing table: EXCLUDED provider -> error,
// a cycle -> error, a DEPRIORITIZED unreviewed provider -> error, an out-of-range threshold ->
// error, and a gate-override attempt -> error — each with its stable code.
func TestRoutingValidator_NegativeTable(t *testing.T) {
	provs := fakeProviders{
		"prospeo":  active("prospeo", "work_email"),
		"hunter":   active("hunter", "work_email"),
		"clearbit": {ID: "clearbit", Status: "EXCLUDED", OpState: "enabled"},
		"apollo":   {ID: "apollo", Status: "DEPRIORITIZED", OpState: "enabled", Compliance: "pending"},
	}
	v := NewValidator(provs, fakeBudgets{}, func() time.Time { return time.Unix(1_800_000_000, 0) })

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "excluded provider referenced",
			payload: `{"schema_version":1,"provider_overrides":{"clearbit":{"mode":"on"}}}`,
			want:    "provider_excluded",
		},
		{
			name:    "deprioritized unreviewed",
			payload: `{"schema_version":1,"provider_overrides":{"apollo":{"mode":"on"}}}`,
			want:    "provider_compliance_unreviewed",
		},
		{
			name:    "unknown provider",
			payload: `{"schema_version":1,"waterfall":{"order":["ghost"]}}`,
			want:    "provider_unknown",
		},
		{
			// order a->b plus a sequential chain b->a forms a cycle across ordering constructs.
			name:    "cycle across ordering constructs",
			payload: `{"schema_version":1,"waterfall":{"order":["prospeo","hunter"],"sequential_chains":[["hunter","prospeo"]]}}`,
			want:    "waterfall_cycle",
		},
		{
			name:    "confidence out of range",
			payload: `{"schema_version":1,"thresholds":{"confidence_target":1.5}}`,
			want:    "threshold_out_of_range",
		},
		{
			name:    "gate override attempt",
			payload: `{"schema_version":1,"override_cost_ceiling":true}`,
			want:    "gate_override_rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := v.Validate(context.Background(), configver.KindRoutingPolicy, "default", json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("internal fault: %v", err)
			}
			errs, _ := reportCodes(t, report)
			if !errs[tc.want] {
				t.Fatalf("want error code %q in report, got errors %v", tc.want, errs)
			}
		})
	}
}

// TestRoutingValidator_CostExceedsBudget proves VR-7 names BOTH numbers (config value + budget).
func TestRoutingValidator_CostExceedsBudget(t *testing.T) {
	v := NewValidator(fakeProviders{}, fakeBudgets{"tenant||day": 1000}, time.Now)
	payload := `{"schema_version":1,"thresholds":{"max_cost_credits_per_record":5000}}`
	report, err := v.Validate(context.Background(), configver.KindRoutingPolicy, "default", json.RawMessage(payload))
	if err != nil {
		t.Fatalf("internal fault: %v", err)
	}
	var rep struct {
		Errors []configver.Finding `json:"errors"`
	}
	_ = json.Unmarshal(report, &rep)
	var msg string
	for _, e := range rep.Errors {
		if e.Code == "cost_exceeds_budget" {
			msg = e.Message
		}
	}
	if msg == "" {
		t.Fatalf("expected cost_exceeds_budget error, got %s", report)
	}
	// The message must name BOTH the config value (5000) and the budget (1000).
	if !contains(msg, "5000") || !contains(msg, "1000") {
		t.Fatalf("VR-7 message must name both numbers, got %q", msg)
	}
}

// TestRoutingValidator_CleanPasses proves a well-formed policy yields zero error-severity findings.
func TestRoutingValidator_CleanPasses(t *testing.T) {
	provs := fakeProviders{"prospeo": active("prospeo", "work_email"), "hunter": active("hunter", "work_email")}
	v := NewValidator(provs, fakeBudgets{"tenant||day": 100000}, time.Now)
	payload := `{"schema_version":1,"provider_overrides":{"prospeo":{"mode":"on"},"hunter":{"mode":"inherit"}},
		"waterfall":{"order":["prospeo","hunter"]},
		"thresholds":{"confidence_target":0.9,"min_confidence":0.5,"max_cost_credits_per_record":50}}`
	report, err := v.Validate(context.Background(), configver.KindRoutingPolicy, "default", json.RawMessage(payload))
	if err != nil {
		t.Fatalf("internal fault: %v", err)
	}
	errs, _ := reportCodes(t, report)
	if len(errs) != 0 {
		t.Fatalf("clean policy produced errors: %v", errs)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
