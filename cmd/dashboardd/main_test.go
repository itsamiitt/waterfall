package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdminFeaturePrefixesRouteToFeatureHandler is the regression guard for a runtime routing gap: the
// R&I feature routes (intent/research/ai/crm) were mounted on the FeatureChain mux but their /v1/admin
// prefixes were missing from adminFeaturePrefixes, so requests fell through to the P0 "/" handler and
// 404ed at runtime — a gap the handler-level and OpenAPI-parity tests did not exercise. This rebuilds the
// same admin-mux composition main() uses and asserts every prefix (and the R&I ones specifically) routes
// to the feature handler rather than the "/" fallback.
func TestAdminFeaturePrefixesRouteToFeatureHandler(t *testing.T) {
	const feature, fallback = "FEATURE", "FALLBACK"
	fh := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(feature)) })

	admin := http.NewServeMux()
	admin.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(fallback)) }))
	for _, p := range adminFeaturePrefixes {
		admin.Handle("/v1/admin/"+p, fh)
		admin.Handle("/v1/admin/"+p+"/", fh)
	}

	route := func(path string) string {
		rw := httptest.NewRecorder()
		admin.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, path, nil))
		return rw.Body.String()
	}

	// Every registered prefix subtree must reach the feature handler.
	for _, p := range adminFeaturePrefixes {
		if got := route("/v1/admin/" + p + "/x"); got != feature {
			t.Errorf("/v1/admin/%s/x routed to %q, want the feature handler", p, got)
		}
	}

	// The R&I features specifically — the exact prefixes that were missing when the gap shipped.
	for _, p := range []string{"intent", "research", "ai", "crm"} {
		if got := route("/v1/admin/" + p + "/anything"); got != feature {
			t.Fatalf("R&I prefix %q does not route to the feature handler — it would 404 at runtime", p)
		}
	}

	// A path with no registered prefix falls through to the P0 handler (sanity: the composition works).
	if got := route("/v1/admin/not-a-feature/x"); got != fallback {
		t.Errorf("unregistered path routed to %q, want the P0 fallback", got)
	}
}
