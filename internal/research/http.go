package research

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/tenant"
)

// Assembler produces a Dossier for a subject. *Orchestrator satisfies it. The HTTP handler depends
// on this interface so it stays unit-testable; the production wiring (the orchestrator with the real
// collect/ai/engine seams) is injected in cmd/enrichapi.
type Assembler interface {
	Assemble(ctx context.Context, subject Subject) (Dossier, error)
}

// HTTPHandler serves the Research API (ADR-0028) on the enrichapi deployable. It reuses the platform
// API conventions (ADR-0012): the tenant flows from the authenticated Principal (G1, never the body),
// Idempotency-Key is required on writes, JSON is snake_case, and errors use the uniform body
// {"error":{"code","message"}}.
type HTTPHandler struct {
	Assembler Assembler
}

// researchRequest is the POST /v1/research body.
type researchRequest struct {
	CompanyDomain  string   `json:"company_domain"`
	CompanyName    string   `json:"company_name"`
	LinkedInURL    string   `json:"linkedin_url"`
	WorkEmail      string   `json:"work_email"`
	Phone          string   `json:"phone"`
	WantedSections []string `json:"wanted_sections"`
}

// Routes registers the Research API endpoints on a standalone mux (for tests / standalone serving).
// The mounting gateway instead sets api.Server.Research = h and applies its own auth/rate-limit/
// instrument wrappers, exactly as for /v1/enrichments.
func (h *HTTPHandler) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/research", h)
}

// ServeHTTP handles POST /v1/research (HTTPHandler implements http.Handler so the gateway can mount
// it directly). This increment serves the SYNCHRONOUS assembly (the ?mode=sync preview): it
// assembles and returns the Dossier inline. The default async 202+job_id flow and
// GET /v1/research/{id} land with research persistence (migration 0015).
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Idempotency-Key") == "" {
		writeErr(w, http.StatusBadRequest, "missing_idempotency_key",
			"the Idempotency-Key header is required on writes")
		return
	}
	var body researchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	subject, ok := body.subject()
	if !ok {
		writeErr(w, http.StatusUnprocessableEntity, "validation_error",
			"at least one of company_domain, company_name, linkedin_url, work_email, phone is required")
		return
	}
	// G1: the tenant flows from the authenticated principal, never the request body.
	if _, err := tenant.FromContext(r.Context()); err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	dossier, err := h.Assembler.Assemble(r.Context(), subject)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "assembly_failed", "the dossier could not be assembled")
		return
	}
	writeJSON(w, http.StatusOK, dossier)
}

// subject builds the resolved Subject from the request, requiring at least one identifier.
func (b researchRequest) subject() (Subject, bool) {
	s := Subject{Domain: strings.TrimSpace(b.CompanyDomain), Name: strings.TrimSpace(b.CompanyName)}
	ids := map[string]string{}
	if v := strings.TrimSpace(b.LinkedInURL); v != "" {
		ids["linkedin_url"] = v
	}
	if v := strings.TrimSpace(b.WorkEmail); v != "" {
		ids["work_email"] = v
	}
	if v := strings.TrimSpace(b.Phone); v != "" {
		ids["phone"] = v
	}
	if len(ids) > 0 {
		s.ResolvedIDs = ids
	}
	if s.Domain == "" && s.Name == "" && len(ids) == 0 {
		return Subject{}, false
	}
	return s, true
}

// --- uniform response helpers (ADR-0012) ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	type errObj struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	writeJSON(w, status, struct {
		Error errObj `json:"error"`
	}{Error: errObj{Code: code, Message: msg}})
}
