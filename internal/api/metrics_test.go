package api_test

import (
	"strings"
	"testing"
)

// TestMetricsEndpoint proves the RED golden signals are exposed and the route label is the
// path TEMPLATE (never a concrete id — no PII/high-cardinality in labels).
func TestMetricsEndpoint(t *testing.T) {
	env := setup(t, nil)

	// Generate some traffic across routes and outcomes.
	do(env.handler, "POST", "/v1/enrichments?mode=sync", "tokA", "m-1", body("p-metrics", 100, 0.85, "work_email"))
	do(env.handler, "GET", "/v1/enrichments/nope", "tokA", "", nil)       // 404
	do(env.handler, "GET", "/v1/records/some-secret-id", "tokA", "", nil) // 200

	rec := do(env.handler, "GET", "/metrics", "", "", nil)
	if rec.Code != 200 {
		t.Fatalf("/metrics should be 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, "# TYPE http_requests_total counter") {
		t.Fatalf("missing http_requests_total type: %s", out)
	}
	if !strings.Contains(out, `http_requests_total{route="/v1/enrichments",method="POST",status="200"} 1`) {
		t.Errorf("missing POST enrichments 200 counter:\n%s", out)
	}
	if !strings.Contains(out, `route="/v1/enrichments/{id}"`) {
		t.Errorf("job route should use the {id} template label, not a concrete id:\n%s", out)
	}
	// The concrete subject id must NOT appear as a label (no PII/high-cardinality).
	if strings.Contains(out, "some-secret-id") {
		t.Errorf("PII/id leaked into metrics labels:\n%s", out)
	}
	if !strings.Contains(out, "http_request_duration_seconds_bucket") {
		t.Errorf("missing duration histogram:\n%s", out)
	}
}
