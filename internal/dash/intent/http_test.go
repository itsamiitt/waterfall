package intent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

// TestIntentDash_AuthAndRBAC exercises the read middleware without a datastore: a request with no
// Principal is 401, and a Principal carrying no recognized role scope is 403 — both reject before the
// service is touched (so a nil Service is safe here). The 200 data path is covered live by the
// service RLS integration test.
func TestIntentDash_AuthAndRBAC(t *testing.T) {
	mux := http.NewServeMux()
	Routes(mux, Deps{}) // nil Service/Auth — middleware-only

	// No principal + no authenticator → 401.
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, intentBase+"/accounts", nil))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("no-principal status = %d, want 401", rw.Code)
	}

	// Principal present but with no role scope → RBAC denies → 403 (before the service).
	req := httptest.NewRequest(http.MethodGet, intentBase+"/accounts", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{TenantID: "t1", UserID: "u1"}))
	rw2 := httptest.NewRecorder()
	mux.ServeHTTP(rw2, req)
	if rw2.Code != http.StatusForbidden {
		t.Fatalf("no-role status = %d, want 403", rw2.Code)
	}

	// Same for the per-account route.
	req3 := httptest.NewRequest(http.MethodGet, intentBase+"/accounts/acme.com", nil)
	rw3 := httptest.NewRecorder()
	mux.ServeHTTP(rw3, req3)
	if rw3.Code != http.StatusUnauthorized {
		t.Fatalf("account no-principal status = %d, want 401", rw3.Code)
	}
}
