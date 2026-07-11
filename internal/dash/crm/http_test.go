package crm

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

// TestCRMDash_AuthAndRBAC exercises the read middleware without a datastore: no Principal → 401; a
// Principal with no recognized role scope → 403; a tenant_user (denied for crm.read) → 403 — all reject
// before the service, so a nil Service is safe. The 200 data path is covered by the RLS integration test.
func TestCRMDash_AuthAndRBAC(t *testing.T) {
	mux := http.NewServeMux()
	Routes(mux, Deps{}) // nil Service/Auth — middleware-only

	// No principal + no authenticator → 401.
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, crmBase+"/connections", nil))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("no-principal status = %d, want 401", rw.Code)
	}

	// Principal with no role scope → 403 (before the service).
	req := httptest.NewRequest(http.MethodGet, crmBase+"/connections", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{TenantID: "t1", UserID: "u1"}))
	rw2 := httptest.NewRecorder()
	mux.ServeHTTP(rw2, req)
	if rw2.Code != http.StatusForbidden {
		t.Fatalf("no-role status = %d, want 403", rw2.Code)
	}

	// tenant_user is denied crm.read (config visibility is a tenant_admin concern) → 403.
	reqTU := httptest.NewRequest(http.MethodGet, crmBase+"/connections/c1", nil)
	reqTU = reqTU.WithContext(tenant.WithPrincipal(reqTU.Context(),
		tenant.Principal{TenantID: "t1", UserID: "u1", Scopes: []string{"role:tenant_user"}}))
	rw3 := httptest.NewRecorder()
	mux.ServeHTTP(rw3, reqTU)
	if rw3.Code != http.StatusForbidden {
		t.Fatalf("tenant_user status = %d, want 403", rw3.Code)
	}
}
