package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

func testServer() *Server { return &Server{idem: newIdemLedger(), logger: slog.Default()} }

func withMeta(r *http.Request, m authMeta) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyMeta{}, m))
}

func decodeErr(t *testing.T, body *httptest.ResponseRecorder) string {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal(body.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error body %q: %v", body.Body.String(), err)
	}
	return e.Error.Code
}

func TestCSRF_MissingHeaderRejected(t *testing.T) {
	s := testServer()
	h := s.csrf(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := withMeta(httptest.NewRequest("POST", "/v1/admin/users", nil), authMeta{session: true, csrfToken: "tok"})
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if code := decodeErr(t, rec); code != codeCSRFRequired {
		t.Fatalf("code = %q, want %q", code, codeCSRFRequired)
	}
}

func TestCSRF_MismatchRejected(t *testing.T) {
	s := testServer()
	h := s.csrf(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := withMeta(httptest.NewRequest("POST", "/x", nil), authMeta{session: true, csrfToken: "right"})
	req.Header.Set("X-CSRF-Token", "wrong")
	rec := httptest.NewRecorder()
	h(rec, req)
	if code := decodeErr(t, rec); rec.Code != 403 || code != codeCSRFInvalid {
		t.Fatalf("got %d/%s, want 403/%s", rec.Code, code, codeCSRFInvalid)
	}
}

func TestCSRF_MatchPasses(t *testing.T) {
	s := testServer()
	ok := false
	h := s.csrf(func(w http.ResponseWriter, r *http.Request) { ok = true; w.WriteHeader(200) })
	req := withMeta(httptest.NewRequest("POST", "/x", nil), authMeta{session: true, csrfToken: "tok"})
	req.Header.Set("X-CSRF-Token", "tok")
	h(httptest.NewRecorder(), req)
	if !ok {
		t.Fatal("handler should have run on matching token")
	}
}

func TestCSRF_JWTExempt(t *testing.T) {
	s := testServer()
	ok := false
	h := s.csrf(func(w http.ResponseWriter, r *http.Request) { ok = true })
	// session=false => JWT path => no CSRF required even without a header
	req := withMeta(httptest.NewRequest("POST", "/x", nil), authMeta{session: false})
	h(httptest.NewRecorder(), req)
	if !ok {
		t.Fatal("JWT path must be CSRF-exempt")
	}
}

func TestRequireRole_DenyAndAllow(t *testing.T) {
	s := testServer()
	h := s.requireRole(rbac.UsersCRUD, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	// tenant_user is denied users.crud
	req := httptest.NewRequest("GET", "/users", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{TenantID: "acme", Scopes: []string{"role:tenant_user"}}))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 403 || decodeErr(t, rec) != codeForbidden {
		t.Fatalf("tenant_user should be forbidden, got %d", rec.Code)
	}

	// tenant_admin is allowed
	req2 := httptest.NewRequest("GET", "/users", nil)
	req2 = req2.WithContext(tenant.WithPrincipal(req2.Context(), tenant.Principal{TenantID: "acme", Scopes: []string{"role:tenant_admin"}}))
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("tenant_admin should be allowed, got %d", rec2.Code)
	}
}

func TestRequireMFA_Pending(t *testing.T) {
	s := testServer()
	h := s.requireMFA(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := withMeta(httptest.NewRequest("GET", "/x", nil), authMeta{mfaOK: false})
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 401 || decodeErr(t, rec) != codeMFARequired {
		t.Fatalf("pending MFA should be 401 mfa_required, got %d", rec.Code)
	}
}

func idemRequest(body, key string) *http.Request {
	r := httptest.NewRequest("POST", "/v1/admin/users", strings.NewReader(body))
	r.Header.Set("Idempotency-Key", key)
	return r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{TenantID: "acme", UserID: "u1"}))
}

func TestIdempotency_MissingKey(t *testing.T) {
	s := testServer()
	h := s.idempotency(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	r := httptest.NewRequest("POST", "/x", strings.NewReader("{}"))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{TenantID: "acme"}))
	rec := httptest.NewRecorder()
	h(rec, r)
	if rec.Code != 400 || decodeErr(t, rec) != codeMissingIdemKey {
		t.Fatalf("missing key should be 400, got %d", rec.Code)
	}
}

func TestIdempotency_DifferentBodyConflict(t *testing.T) {
	s := testServer()
	calls := 0
	h := s.idempotency(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.ReadAll(r.Body) // handler can re-read the buffered body
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	})

	rec1 := httptest.NewRecorder()
	h(rec1, idemRequest(`{"email":"a@x"}`, "K"))
	if rec1.Code != 201 {
		t.Fatalf("first call status = %d, want 201", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h(rec2, idemRequest(`{"email":"b@x"}`, "K")) // same key, different body
	if rec2.Code != http.StatusConflict || decodeErr(t, rec2) != codeIdempotencyReuse {
		t.Fatalf("reuse with different body = %d/%s, want 409/%s", rec2.Code, decodeErr(t, rec2), codeIdempotencyReuse)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 (second was rejected pre-handler)", calls)
	}
}

func TestIdempotency_SameBodyReplays(t *testing.T) {
	s := testServer()
	calls := 0
	h := s.idempotency(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	})
	h(httptest.NewRecorder(), idemRequest(`{"email":"a@x"}`, "K"))
	rec := httptest.NewRecorder()
	h(rec, idemRequest(`{"email":"a@x"}`, "K")) // identical replay
	if rec.Code != 201 || rec.Body.String() != `{"id":"x"}` {
		t.Fatalf("replay = %d %q, want 201 stored body", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("replay must carry Idempotency-Replayed: true")
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 (replay served from ledger)", calls)
	}
}
