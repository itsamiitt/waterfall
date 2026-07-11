package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

type stubResearch struct{ called bool }

func (s *stubResearch) Research(w http.ResponseWriter, _ *http.Request) {
	s.called = true
	writeJSON(w, http.StatusOK, map[string]string{"dossier_id": "d1"})
}

func (s *stubResearch) Run(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"run_id": "run-1", "status": "queued"})
}

func (s *stubResearch) Dossier(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"dossier_id": "d1"})
}

// TestResearchRoute_MountedAndProtected proves the gateway mounts POST /v1/research behind the same
// auth as enrichment: no credential → 401 (handler never runs); a valid credential with the write
// scope reaches the handler (ADR-0028 mount).
func TestResearchRoute_MountedAndProtected(t *testing.T) {
	stub := &stubResearch{}
	srv := &Server{
		Auth: NewStaticAuthenticator(map[string]tenant.Principal{
			"tok": {TenantID: "t1", UserID: "u1", Scopes: []string{"enrich:write"}},
		}),
		Research:   stub,
		WriteScope: "enrich:write",
	}
	h := srv.Handler()

	// No auth → 401, handler not reached.
	req := httptest.NewRequest(http.MethodPost, "/v1/research", strings.NewReader(`{"company_domain":"acme.com"}`))
	req.Header.Set("Idempotency-Key", "k1")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", rw.Code)
	}
	if stub.called {
		t.Fatal("research handler ran without authentication")
	}

	// Auth + scope → reaches the handler (200).
	req2 := httptest.NewRequest(http.MethodPost, "/v1/research", strings.NewReader(`{"company_domain":"acme.com"}`))
	req2.Header.Set("Idempotency-Key", "k1")
	req2.Header.Set("Authorization", "Bearer tok")
	rw2 := httptest.NewRecorder()
	h.ServeHTTP(rw2, req2)
	if rw2.Code != http.StatusOK || !stub.called {
		t.Fatalf("authed status = %d called=%v, want 200/true; body=%s", rw2.Code, stub.called, rw2.Body.String())
	}
}

// TestResearchRoute_RequiresWriteScope proves the route enforces the configured write scope (403 for
// a principal that lacks it) — same posture as enrichment submit.
func TestResearchRoute_RequiresWriteScope(t *testing.T) {
	stub := &stubResearch{}
	srv := &Server{
		Auth: NewStaticAuthenticator(map[string]tenant.Principal{
			"tok": {TenantID: "t1", UserID: "u1"}, // no scopes
		}),
		Research:   stub,
		WriteScope: "enrich:write",
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/research", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	rw := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rw, req)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("no-scope status = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}
	if stub.called {
		t.Fatal("handler ran without the required scope")
	}
}

// TestResearchRoute_OffByDefault proves the route is absent when Research is not set (404).
func TestResearchRoute_OffByDefault(t *testing.T) {
	srv := &Server{Auth: NewStaticAuthenticator(map[string]tenant.Principal{"tok": {TenantID: "t1"}})}
	req := httptest.NewRequest(http.MethodPost, "/v1/research", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	rw := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rw, req)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when Research is unset", rw.Code)
	}
}
