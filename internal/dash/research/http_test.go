package research

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

// TestResearchDash_AuthAndRBAC exercises the read middleware without a datastore: no Principal → 401,
// a Principal with no recognized role scope → 403 — both reject before the service (nil Service safe).
func TestResearchDash_AuthAndRBAC(t *testing.T) {
	mux := http.NewServeMux()
	Routes(mux, Deps{})

	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, researchBase+"/dossiers", nil))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("no-principal status = %d, want 401", rw.Code)
	}

	req := httptest.NewRequest(http.MethodGet, researchBase+"/dossiers", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{TenantID: "t1", UserID: "u1"}))
	rw2 := httptest.NewRecorder()
	mux.ServeHTTP(rw2, req)
	if rw2.Code != http.StatusForbidden {
		t.Fatalf("no-role status = %d, want 403", rw2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, researchBase+"/dossiers/d-1", nil)
	rw3 := httptest.NewRecorder()
	mux.ServeHTTP(rw3, req3)
	if rw3.Code != http.StatusUnauthorized {
		t.Fatalf("dossier no-principal status = %d, want 401", rw3.Code)
	}
}
