package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// DossierStore persists and reads Dossiers. *Store satisfies it; a nil Store disables persistence
// (the sync endpoint still returns the assembled Dossier, and GET /v1/dossiers/{domain} is 404).
type DossierStore interface {
	SaveDossier(ctx context.Context, dossierID, subjectKey string, d Dossier) error
	LatestBySubject(ctx context.Context, subjectKey string) (Dossier, bool, error)
}

// RunSubmitter enqueues a research run for async assembly. *Runner satisfies it.
type RunSubmitter interface {
	Submit(ctx context.Context, runID string, subject Subject) bool
}

// RunStore records and reads run lifecycle rows (research_runs). *Store satisfies it. When both Runs and
// Runner are set, POST /v1/research is async by default (202 + run_id) and GET /v1/research/{id} serves
// status; without them the endpoint serves the synchronous inline assembly.
type RunStore interface {
	CreateRun(ctx context.Context, runID, subjectKey, configVersion string) (bool, error)
	GetRun(ctx context.Context, runID string) (Run, bool, error)
}

type HTTPHandler struct {
	Assembler Assembler
	Store     DossierStore // optional; enables persistence + GET /v1/dossiers/{domain}
	Runs      RunStore     // optional; with Runner, enables async POST + GET /v1/research/{id}
	Runner    RunSubmitter // optional; the async worker
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
	mux.HandleFunc("POST /v1/research", h.Research)
	mux.HandleFunc("GET /v1/research/{id}", h.Run)
	mux.HandleFunc("GET /v1/dossiers/{domain}", h.Dossier)
}

// Research handles POST /v1/research. When the run store + worker are wired it is ASYNC by default
// (ADR-0028): it records a queued run, enqueues it, and returns 202 + run_id (a retry with the same
// Idempotency-Key returns the existing run — G2). `?mode=sync`, or a deployment without the run lane
// (memory mode), serves the inline capped-budget assembly instead.
func (h *HTTPHandler) Research(w http.ResponseWriter, r *http.Request) {
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
	principal, err := tenant.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}

	async := h.Runs != nil && h.Runner != nil && r.URL.Query().Get("mode") != "sync"
	if async {
		h.submitAsync(w, r, principal, subject)
		return
	}
	h.assembleSync(w, r, subject)
}

// submitAsync records a queued run and enqueues it, returning 202 + run_id. A retry with the same
// Idempotency-Key resolves to the same run_id, so CreateRun is a no-op and the existing run's status is
// returned instead of starting a second assembly (G2).
func (h *HTTPHandler) submitAsync(w http.ResponseWriter, r *http.Request, principal tenant.Principal, subject Subject) {
	runID := deriveRunID(principal.TenantID, r.Header.Get("Idempotency-Key"))
	created, err := h.Runs.CreateRun(r.Context(), runID, subjectID(subject), "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not record the research run")
		return
	}
	status := RunQueued
	if created {
		h.Runner.Submit(r.Context(), runID, subject) // backpressure: run stays queued if the queue is full
	} else if run, ok, gerr := h.Runs.GetRun(r.Context(), runID); gerr == nil && ok {
		status = run.Status // duplicate submission: reflect the existing run's progress
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": runID, "status": status})
}

// assembleSync assembles the Dossier inline, persists it when a Store is configured, and returns it.
func (h *HTTPHandler) assembleSync(w http.ResponseWriter, r *http.Request, subject Subject) {
	dossier, err := h.Assembler.Assemble(r.Context(), subject)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "assembly_failed", "the dossier could not be assembled")
		return
	}
	dossier.DossierID = subjectID(subject)
	// Persist the assembled Dossier (latest-per-subject) when a Store is configured. A persistence
	// failure is logged in the Dossier, not fatal — the caller still gets the assembled result.
	if h.Store != nil {
		if serr := h.Store.SaveDossier(r.Context(), dossier.DossierID, subjectID(subject), dossier); serr != nil {
			dossier.ProcessingLog = append(dossier.ProcessingLog, "persist: error: "+serr.Error())
		} else {
			dossier.ProcessingLog = append(dossier.ProcessingLog, "persist: stored dossier "+dossier.DossierID)
		}
	}
	writeJSON(w, http.StatusOK, dossier)
}

// runResponse is the GET /v1/research/{id} body: the run's lifecycle plus the assembled Dossier once done.
type runResponse struct {
	RunID         string   `json:"run_id"`
	SubjectKey    string   `json:"subject_key"`
	Status        string   `json:"status"`
	ConfigVersion string   `json:"config_version"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	Dossier       *Dossier `json:"dossier,omitempty"`
}

// Run handles GET /v1/research/{id}: the status of an async research run, plus the assembled Dossier once
// it is done. Tenant-scoped (G1) — a run belonging to another tenant is 404. 404 when async is disabled.
func (h *HTTPHandler) Run(w http.ResponseWriter, r *http.Request) {
	if _, err := tenant.FromContext(r.Context()); err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	if h.Runs == nil {
		writeErr(w, http.StatusNotFound, "not_found", "async research is not enabled")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusUnprocessableEntity, "validation_error", "run id is required")
		return
	}
	run, ok, err := h.Runs.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "run lookup failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no research run with this id")
		return
	}
	resp := runResponse{
		RunID: run.RunID, SubjectKey: run.SubjectKey, Status: run.Status,
		ConfigVersion: run.ConfigVersion, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
	}
	if run.Status == RunDone && h.Store != nil {
		if d, found, derr := h.Store.LatestBySubject(r.Context(), run.SubjectKey); derr == nil && found {
			resp.Dossier = &d
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// deriveRunID makes a stable, non-secret run id from the tenant + Idempotency-Key, so a retried submission
// maps to the same run (G2) without echoing the client's key on the wire.
func deriveRunID(tenantID, idemKey string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + idemKey))
	return "run_" + hex.EncodeToString(sum[:16])
}

// Dossier handles GET /v1/dossiers/{domain}: the freshest stored Dossier for a Company within the
// caller's tenant (G1). Returns 404 when persistence is disabled or no Dossier exists.
func (h *HTTPHandler) Dossier(w http.ResponseWriter, r *http.Request) {
	if _, err := tenant.FromContext(r.Context()); err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	if h.Store == nil {
		writeErr(w, http.StatusNotFound, "not_found", "dossier persistence is not enabled")
		return
	}
	domainKey := strings.TrimSpace(r.PathValue("domain"))
	if domainKey == "" {
		writeErr(w, http.StatusUnprocessableEntity, "validation_error", "domain is required")
		return
	}
	d, ok, err := h.Store.LatestBySubject(r.Context(), domainKey)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "dossier lookup failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no dossier for this domain")
		return
	}
	writeJSON(w, http.StatusOK, d)
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
