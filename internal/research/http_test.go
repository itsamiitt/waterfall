package research

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

type fakeAssembler struct {
	d   Dossier
	err error
}

func (f fakeAssembler) Assemble(_ context.Context, _ Subject) (Dossier, error) { return f.d, f.err }

func doPost(t *testing.T, h *HTTPHandler, body string, withKey, withPrincipal bool) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/v1/research", strings.NewReader(body))
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

func TestPostResearch_SyncReturnsDossier(t *testing.T) {
	h := &HTTPHandler{Assembler: fakeAssembler{d: Dossier{
		CompanyProfile: map[string]string{"name": "Acme"},
		AISummary:      "acme makes widgets",
	}}}
	rw := doPost(t, h, `{"company_domain":"acme.com"}`, true, true)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	var d Dossier
	if err := json.Unmarshal(rw.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.CompanyProfile["name"] != "Acme" || d.AISummary != "acme makes widgets" {
		t.Fatalf("dossier = %+v", d)
	}
}

func TestPostResearch_Errors(t *testing.T) {
	h := &HTTPHandler{Assembler: fakeAssembler{}}
	cases := []struct {
		name          string
		body          string
		withKey       bool
		withPrincipal bool
		wantCode      int
		wantErrCode   string
	}{
		{"missing_idem_key", `{"company_domain":"acme.com"}`, false, true, http.StatusBadRequest, "missing_idempotency_key"},
		{"no_identifiers", `{}`, true, true, http.StatusUnprocessableEntity, "validation_error"},
		{"no_principal", `{"company_domain":"acme.com"}`, true, false, http.StatusUnauthorized, "unauthorized"},
		{"unknown_field", `{"nope":1}`, true, true, http.StatusBadRequest, "invalid_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rw := doPost(t, h, tc.body, tc.withKey, tc.withPrincipal)
			if rw.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rw.Code, tc.wantCode, rw.Body.String())
			}
			var e struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rw.Body.Bytes(), &e); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if e.Error.Code != tc.wantErrCode {
				t.Fatalf("error.code = %q, want %q", e.Error.Code, tc.wantErrCode)
			}
		})
	}
}

// TestPostResearch_EndToEnd drives the HTTP endpoint through the REAL orchestrator + enrichment engine
// (in-memory store + mock provider) + a stub AI — proving domain→dossier works over HTTP.
func TestPostResearch_EndToEnd(t *testing.T) {
	nameP := providertest.New("vendor-name", "Acme", 0.85, 2, domain.FieldCompanyName)
	eng := engine.New(store.NewMemory(), []provider.Adapter{nameP})
	enr := EngineEnricher{Engine: eng, Planner: router.New(nameP), CostCeiling: 50, ConfidenceTarget: 0.9, ConfigVersion: "v1"}
	o := NewOrchestrator(enr, nil, fakeAI{raw: []byte(`{"summary":"ok"}`), model: "m"})
	o.now = fixedClock()

	h := &HTTPHandler{Assembler: o}
	rw := doPost(t, h, `{"company_domain":"acme.com"}`, true, true)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	var d Dossier
	if err := json.Unmarshal(rw.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Firmographics["company_name"] != "Acme" || d.AISummary != "ok" {
		t.Fatalf("dossier firmographics=%v summary=%q", d.Firmographics, d.AISummary)
	}
	if len(d.Provenance) == 0 {
		t.Fatalf("expected provenance rows")
	}
}
