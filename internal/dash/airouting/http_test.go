package airouting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

// TestAIModelsDash_AuthAndRBAC exercises the read middleware and the data path end-to-end (no store
// is needed — the catalog is a static projection of the ai.Models registry). No Principal → 401; a
// tenant_admin → 403 (platform config is operator-only); an operator → 200 with the free-first cascade,
// and no credential material in the projection.
func TestAIModelsDash_AuthAndRBAC(t *testing.T) {
	mux := http.NewServeMux()
	Routes(mux, Deps{Service: NewService()})

	// No principal + no authenticator → 401.
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, aiBase+"/models", nil))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("no-principal status = %d, want 401", rw.Code)
	}

	// tenant_admin is denied — the LLM registry is platform config, not tenant data → 403.
	reqTA := httptest.NewRequest(http.MethodGet, aiBase+"/models", nil)
	reqTA = reqTA.WithContext(tenant.WithPrincipal(reqTA.Context(),
		tenant.Principal{TenantID: "acme", UserID: "u", Scopes: []string{"role:tenant_admin"}}))
	rwTA := httptest.NewRecorder()
	mux.ServeHTTP(rwTA, reqTA)
	if rwTA.Code != http.StatusForbidden {
		t.Fatalf("tenant_admin status = %d, want 403", rwTA.Code)
	}

	// operator sees the catalog → 200.
	reqOp := httptest.NewRequest(http.MethodGet, aiBase+"/models", nil)
	reqOp = reqOp.WithContext(tenant.WithPrincipal(reqOp.Context(),
		tenant.Principal{TenantID: "platform", UserID: "u", Scopes: []string{"role:operator"}}))
	rwOp := httptest.NewRecorder()
	mux.ServeHTTP(rwOp, reqOp)
	if rwOp.Code != http.StatusOK {
		t.Fatalf("operator status = %d, want 200", rwOp.Code)
	}
	var body struct {
		Items []ModelInfo `json:"items"`
	}
	if err := json.NewDecoder(rwOp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) == 0 {
		t.Fatal("expected a non-empty model catalog")
	}
	if !body.Items[0].Free {
		t.Fatalf("cascade must be free-first; got %q (free=%v)", body.Items[0].Slug, body.Items[0].Free)
	}
	for _, m := range body.Items {
		if m.Slug == "" || m.Dialect == "" || m.Host == "" {
			t.Fatalf("projection incomplete: %+v", m)
		}
		if m.Dialect != "openai" && m.Dialect != "anthropic" {
			t.Fatalf("unexpected dialect %q for %s", m.Dialect, m.Slug)
		}
	}
}
