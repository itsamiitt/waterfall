package api

import (
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/tenant"
)

// submit handles POST /v1/enrichments. It requires an Idempotency-Key (docs/07,
// ADR-0012), validates the body, derives a deterministic job id from (tenant, key), and
// either runs the job inline (?mode=sync) or enqueues it for a worker (default async).
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key",
			"the Idempotency-Key header is required on writes")
		return
	}

	var body submitRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	req, verr := body.toDomain()
	if verr != "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", verr)
		return
	}

	// Principal is guaranteed present by protected(); tenant flows from it (G1).
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	req.JobID = job.DeriveID(p.TenantID, idemKey)
	fp := job.Fingerprint(req)
	ctx := r.Context()

	// Idempotent submission: a repeat of the same key returns the same job. A repeat with
	// a DIFFERENT body under the same key is a client error (409).
	if existing, ok, err := s.Jobs.Get(ctx, req.JobID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "job lookup failed")
		return
	} else if ok {
		if existing.Fingerprint != fp {
			writeError(w, http.StatusConflict, "idempotency_key_reuse",
				"Idempotency-Key was reused with a different request body")
			return
		}
		writeJSON(w, statusCodeForStatus(existing.Status), toJobResponse(existing))
		return
	}

	j := &job.Job{
		ID:             req.JobID,
		TenantID:       p.TenantID,
		IdempotencyKey: idemKey,
		Fingerprint:    fp,
		Principal:      p,
		Req:            req,
		Priority:       body.priority(),
		Status:         job.StatusQueued,
		CreatedAt:      s.now(),
		UpdatedAt:      s.now(),
	}

	// Sync mode: run inline and return the terminal outcome.
	if r.URL.Query().Get("mode") == "sync" {
		s.Dispatcher.Run(j)
		final, _, _ := s.Jobs.Get(ctx, j.ID)
		if final == nil {
			final = j
		}
		writeJSON(w, statusCodeForStatus(final.Status), toJobResponse(final))
		return
	}

	// Async mode: hand the job to the configured Submitter (in-process queue or durable
	// outbox). A shed (accepted=false) becomes 429; the durable submitter does not shed.
	accepted, err := s.Submitter.Submit(ctx, j)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "submit failed")
		return
	}
	if !accepted {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "queue_full",
			"the enrichment queue is saturated; retry shortly")
		return
	}
	// Respond from locals only — the worker/relay now owns j and may mutate it concurrently.
	writeJSON(w, http.StatusAccepted, jobResponse{JobID: req.JobID, Status: string(job.StatusQueued)})
}

// getJob handles GET /v1/enrichments/{id}. A job belonging to another tenant is reported
// as 404 (no cross-tenant existence disclosure — G1).
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok, err := s.Jobs.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "job lookup failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	writeJSON(w, http.StatusOK, toJobResponse(j))
}

// getRecord handles GET /v1/records/{subjectID}, returning the current best value per
// Field for the subject within the caller's tenant.
func (s *Server) getRecord(w http.ResponseWriter, r *http.Request) {
	subjectID := r.PathValue("subjectID")
	cur, err := s.Records.Current(r.Context(), subjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "record lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, toRecordsResponse(subjectID, cur))
}
