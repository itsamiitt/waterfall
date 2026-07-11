package intent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

type fakeScoreStore struct {
	scores map[string][]ClassScore
	err    error
}

func (f fakeScoreStore) GetByAccount(_ context.Context, account string) ([]ClassScore, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.scores[account], nil
}

func doGet(t *testing.T, h *HTTPHandler, path string, withPrincipal bool) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Routes(mux)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if withPrincipal {
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{TenantID: "t1", UserID: "u1"}))
	}
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)
	return rw
}

func TestIntentAccounts_ReturnsScores(t *testing.T) {
	store := fakeScoreStore{scores: map[string][]ClassScore{
		"acme.com": {{Class: ClassHiring, Score: 0.8, Confidence: 0.7, SignalCount: 2}},
	}}
	rw := doGet(t, &HTTPHandler{Store: store}, "/v1/intent/accounts/acme.com", true)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rw.Code, rw.Body.String())
	}
	var resp accountResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Account != "acme.com" || len(resp.Scores) != 1 || resp.Scores[0].Class != ClassHiring {
		t.Fatalf("response = %+v", resp)
	}
}

func TestIntentAccounts_EmptyIs200(t *testing.T) {
	rw := doGet(t, &HTTPHandler{Store: fakeScoreStore{scores: map[string][]ClassScore{}}}, "/v1/intent/accounts/nointent.com", true)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty is valid)", rw.Code)
	}
	var resp accountResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Account != "nointent.com" || len(resp.Scores) != 0 {
		t.Fatalf("response = %+v, want empty scores", resp)
	}
}

func TestIntentAccounts_Errors(t *testing.T) {
	// No store → 404.
	if rw := doGet(t, &HTTPHandler{}, "/v1/intent/accounts/acme.com", true); rw.Code != http.StatusNotFound {
		t.Fatalf("no-store status = %d, want 404", rw.Code)
	}
	// No principal → 401.
	if rw := doGet(t, &HTTPHandler{Store: fakeScoreStore{}}, "/v1/intent/accounts/acme.com", false); rw.Code != http.StatusUnauthorized {
		t.Fatalf("no-principal status = %d, want 401", rw.Code)
	}
	// Store error → 500.
	if rw := doGet(t, &HTTPHandler{Store: fakeScoreStore{err: errors.New("boom")}}, "/v1/intent/accounts/acme.com", true); rw.Code != http.StatusInternalServerError {
		t.Fatalf("store-error status = %d, want 500", rw.Code)
	}
}

type fakeRefresher struct {
	scores        []ClassScore
	err           error
	calledAccount string
}

func (f *fakeRefresher) Refresh(_ context.Context, account string) ([]ClassScore, error) {
	f.calledAccount = account
	return f.scores, f.err
}

func doPost(t *testing.T, h *HTTPHandler, body string, withKey, withPrincipal bool) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/v1/intent/refresh", strings.NewReader(body))
	if withKey {
		req.Header.Set("Idempotency-Key", "k1")
	}
	if withPrincipal {
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{TenantID: "t1", UserID: "u1"}))
	}
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)
	return rw
}

func TestIntentRefresh_Runs(t *testing.T) {
	ref := &fakeRefresher{scores: []ClassScore{{Class: ClassHiring, Score: 0.8, Confidence: 0.7}}}
	rw := doPost(t, &HTTPHandler{Refresher: ref}, `{"company_domain":"acme.com"}`, true, true)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rw.Code, rw.Body.String())
	}
	if ref.calledAccount != "acme.com" {
		t.Fatalf("refresher called with %q, want acme.com", ref.calledAccount)
	}
	var resp accountResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Account != "acme.com" || len(resp.Scores) != 1 || resp.Scores[0].Class != ClassHiring {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestIntentRefresh_Errors(t *testing.T) {
	h := &HTTPHandler{Refresher: &fakeRefresher{}}
	if rw := doPost(t, h, `{"company_domain":"acme.com"}`, false, true); rw.Code != http.StatusBadRequest {
		t.Fatalf("missing-idem-key status = %d, want 400", rw.Code)
	}
	if rw := doPost(t, h, `{}`, true, true); rw.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no-account status = %d, want 422", rw.Code)
	}
	if rw := doPost(t, h, `{"company_domain":"acme.com"}`, true, false); rw.Code != http.StatusUnauthorized {
		t.Fatalf("no-principal status = %d, want 401", rw.Code)
	}
	// No refresher configured → 404.
	if rw := doPost(t, &HTTPHandler{}, `{"company_domain":"acme.com"}`, true, true); rw.Code != http.StatusNotFound {
		t.Fatalf("no-refresher status = %d, want 404", rw.Code)
	}
}
