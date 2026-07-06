package httpx

import (
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/tenant"
)

// CtxAuthenticator is a passthrough Authenticator for feature packages mounted behind
// FeatureChain: the chain's authenticate middleware has already resolved and bound the verified
// Principal into the request context (single real authentication), so a feature package's own
// authenticate step just reads it back. This keeps CSRF / MFA-gate / IP-allowlist enforcement in
// one place (the shared chain) while the feature package still owns RBAC, idempotency, and audit.
// It satisfies the identical Authenticate(r) (tenant.Principal, error) interface that the
// providers and keys packages declare.
type CtxAuthenticator struct{}

// Authenticate returns the Principal that FeatureChain's authenticate middleware bound into ctx.
func (CtxAuthenticator) Authenticate(r *http.Request) (tenant.Principal, error) {
	return tenant.FromContext(r.Context())
}

// featureLabels bounds the instrument route label for feature routes to a small fixed set, so the
// metric's route cardinality never grows with ids (doc 10 no-unbounded-labels rule).
var featureLabels = map[string]string{
	"providers":      "providers",
	"keys":           "keys",
	"key-pools":      "key-pools",
	"key-imports":    "key-imports",
	"bulk-jobs":      "bulk-jobs",
	"rotation":       "rotation",
	"routing":        "routing",
	"workflows":      "workflows",
	"config":         "config",
	"health":         "health",
	"approvals":      "approvals",
	"change-history": "change-history",
	"queues":         "queues",
	"dead-letters":   "dead-letters",
	"jobs":           "jobs",
	"workers":        "workers",
	"cost":           "cost",
	"budgets":        "budgets",
	"alerts":         "alerts",
	"streams":        "streams",
	"overview":       "overview",
	"search":         "search",
	"meta":           "meta",
	"tenants":        "tenants",
}

// featureLabel derives the bounded metric label from the request path's segment after /v1/admin/.
func featureLabel(path string) string {
	rest := strings.TrimPrefix(path, basePath+"/")
	seg := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		seg = rest[:i]
	}
	if lbl, ok := featureLabels[seg]; ok {
		return "/v1/admin/" + lbl
	}
	return "/v1/admin/feature"
}

// FeatureChain wraps a feature route mux with the shared cross-cutting middleware so mounted
// feature surfaces (providers, keys, …) get the same protections as the P0 routes without
// re-authenticating: instrument (bounded label) -> authenticate (binds Principal + auth meta into
// ctx) -> ip-allowlist -> csrf (mutating cookie-session requests) -> require-MFA. The feature
// package then applies its own RBAC, idempotency, step-up, and audit using the ctx Principal
// (via CtxAuthenticator). Order matches the P0 per-route chain (doc 05 §4/§5/§6, doc 02 §2).
func (s *Server) FeatureChain(next http.Handler) http.Handler {
	inner := s.authenticate(s.ipAllow(s.csrf(s.requireMFA(next.ServeHTTP))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.instrument(featureLabel(r.URL.Path), inner)(w, r)
	})
}
