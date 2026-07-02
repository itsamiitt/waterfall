package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/tenant"
)

// DeadLetterAdmin lists and redrives a tenant's parked ("poison") jobs — those the outbox
// stopped redelivering after exceeding the max delivery attempts. Implemented by the Postgres
// outbox; optional, so the /v1/dead-letters routes exist only when an admin is wired in.
type DeadLetterAdmin interface {
	// DeadLetters returns the CALLER tenant's dead-lettered jobs (RLS-scoped), newest first.
	DeadLetters(ctx context.Context, limit int) ([]DeadLetterItem, error)
	// Redrive resets one of the CALLER tenant's dead-lettered jobs so it is re-delivered.
	// Returns false if no such dead job exists for this tenant.
	Redrive(ctx context.Context, jobID string) (bool, error)
}

// DeadLetterItem is one parked job in the API representation.
type DeadLetterItem struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
	UpdatedAt string `json:"updated_at"`
}

type deadLettersResponse struct {
	DeadLetters []DeadLetterItem `json:"dead_letters"`
}

// getDeadLetters handles GET /v1/dead-letters. Tenant scope flows from the principal (G1), so a
// tenant sees only its own parked jobs.
func (s *Server) getDeadLetters(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := s.DeadLetters.DeadLetters(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "dead-letter lookup failed")
		return
	}
	if items == nil {
		items = []DeadLetterItem{}
	}
	writeJSON(w, http.StatusOK, deadLettersResponse{DeadLetters: items})
}

// redriveDeadLetter handles POST /v1/dead-letters/{id}/redrive. It resets a parked job so the
// relay re-delivers it (after the underlying bug is fixed). A write: requires the write scope,
// tenant-scoped (a tenant can only redrive its own), and audit-logged.
func (s *Server) redriveDeadLetter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := tenant.FromContext(r.Context())
	ok, err := s.DeadLetters.Redrive(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "redrive failed")
		return
	}
	if !ok {
		s.log().Info("dlq_redrive", "tenant", p.TenantID, "user", p.UserID, "job", id, "result", "not_found")
		writeError(w, http.StatusNotFound, "not_found", "no dead-lettered job with that id")
		return
	}
	if s.redriveCount != nil {
		s.redriveCount.Inc()
	}
	// Audit: who redrove what (a deliberate re-execution of a previously-failing job).
	s.log().Warn("dlq_redrive", "tenant", p.TenantID, "user", p.UserID, "job", id, "result", "redriven")
	writeJSON(w, http.StatusOK, map[string]string{"job_id": id, "status": "redriven"})
}
