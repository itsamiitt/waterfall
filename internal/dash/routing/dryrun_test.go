package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/configver"
)

// failTransport fails the test on ANY outbound request — the zero-egress proof (acceptance #3).
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Fatalf("dry-run made an egress request to %s — the simulator must be read-only (zero egress)", r.URL)
	return nil, nil
}

// TestRoutingDryRun_ZeroEgress proves the routing dry-run runs the real Planner against the draft
// payload and returns the by-Field Provider order + expected cost while making ZERO egress: the
// injected RoundTripper is wired into every synthetic adapter yet is never invoked.
func TestRoutingDryRun_ZeroEgress(t *testing.T) {
	provs := fakeProviders{
		"prospeo":  cap3("prospeo", "work_email", 2, 0.88),
		"hunter":   cap3("hunter", "work_email", 3, 0.81),
		"zoominfo": cap3("zoominfo", "work_email", 9, 0.90),
	}
	client := &http.Client{Transport: failTransport{t: t}}
	dr := NewDryRunner(provs, client, nil)

	payload := json.RawMessage(`{"schema_version":1,
		"provider_overrides":{"prospeo":{"mode":"on"},"hunter":{"mode":"on"},"zoominfo":{"mode":"on"}},
		"waterfall":{"order":["prospeo","hunter","zoominfo"]}}`)

	out, err := dr.DryRun(context.Background(), "default", payload, configver.DryRunRequest{Want: []string{"work_email"}})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	m := out.(map[string]any)
	byField := m["by_field"].(map[string][]configver.PlanStep)
	steps := byField["work_email"]
	if len(steps) != 3 {
		t.Fatalf("expected 3 planned providers for work_email, got %d (%v)", len(steps), steps)
	}
	// Reservation value = expected confidence per credit: prospeo (0.88/2=.44) > hunter (0.81/3=.27)
	// > zoominfo (0.90/9=.10). The planner orders cheapest-high-yield first.
	if steps[0].Provider != "prospeo" {
		t.Fatalf("expected prospeo first by reservation value, got %q", steps[0].Provider)
	}
	if steps[0].CostCredits != 2 || steps[0].ExpectedConfidence != 0.88 {
		t.Fatalf("planned cost/confidence wrong: %+v", steps[0])
	}
	if mc, ok := m["max_committed_credits"].(int64); !ok || mc != 14 {
		t.Fatalf("max_committed_credits = %v, want 14", m["max_committed_credits"])
	}
}

// TestRoutingDryRun_OffExcludesProvider proves a mode:"off" override drops a Provider from the plan.
func TestRoutingDryRun_OffExcludesProvider(t *testing.T) {
	provs := fakeProviders{
		"prospeo": cap3("prospeo", "work_email", 2, 0.88),
		"hunter":  cap3("hunter", "work_email", 3, 0.81),
	}
	dr := NewDryRunner(provs, &http.Client{Transport: failTransport{t: t}}, nil)
	payload := json.RawMessage(`{"schema_version":1,
		"provider_overrides":{"prospeo":{"mode":"on"},"hunter":{"mode":"off"}}}`)
	out, err := dr.DryRun(context.Background(), "default", payload, configver.DryRunRequest{Want: []string{"work_email"}})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	steps := out.(map[string]any)["by_field"].(map[string][]configver.PlanStep)["work_email"]
	if len(steps) != 1 || steps[0].Provider != "prospeo" {
		t.Fatalf("off override should have dropped hunter, got %v", steps)
	}
}

func cap3(id, field string, cost int64, conf float64) configver.ProviderInfo {
	return configver.ProviderInfo{
		ID: id, Status: "ACTIVE-CANDIDATE", OpState: "enabled",
		Capabilities: []configver.Capability{{Field: field, Cost: cost, ExpectedConfidence: conf}},
	}
}
