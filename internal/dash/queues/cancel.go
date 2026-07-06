package queues

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Bulk-job cancellation (OI-API-4, doc 15 §T3). POST /v1/admin/bulk-jobs/{id}/cancel sets the
// cooperative cancel flag on the durable bulk_jobs row; each executor (keys import loop, queues
// replay loop) polls it between waves and stops at a clean terminal 'cancelled', retaining rows it
// already committed (G2 makes a resubmit safe). The endpoint is kind-agnostic — it lives beside the
// kind-agnostic GET /bulk-jobs/{id} reader — and applies RBAC per the job's kind. The orchestrator
// mounts it via CancelRoute (this package never mounts routes into Routes()).

// ErrJobTerminal reports a cancel of an already-terminal (or never-in-flight) bulk job — a 409/no-op
// signal, distinct from ErrNotFound (a missing or cross-Tenant job, which is 404).
var ErrJobTerminal = errors.New("queues: bulk job is already terminal")

// RequestCancel sets cancel_requested=true on an in-flight (queued|running) bulk_jobs row, RLS-scoped
// to the caller's Tenant. It is idempotent: cancelling an already-cancelling running job succeeds
// again (still in-flight). A terminal job returns ErrJobTerminal; a missing/cross-Tenant job returns
// ErrNotFound (G1: cross-Tenant existence reads as not-found). Returns the job kind on success.
func (s *Service) RequestCancel(ctx context.Context, jobID string) (string, error) {
	kind := ""
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, e := c.QueryParams(`update bulk_jobs set cancel_requested=true
			where id=$1::uuid and status in ('queued','running') returning kind`, jobID)
		if e != nil {
			return e
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			kind = s0(res.Rows[0][0])
			return nil
		}
		// Nothing updated: the row is terminal or absent — distinguish for the right HTTP status.
		chk, e2 := c.QueryParams(`select 1 from bulk_jobs where id=$1::uuid`, jobID)
		if e2 != nil {
			return e2
		}
		if len(chk.Rows) == 0 || chk.Rows[0][0] == nil {
			return ErrNotFound
		}
		return ErrJobTerminal
	})
	if err != nil {
		return "", err
	}
	s.appendAudit(ctx, "bulk_job_cancel", "bulk_jobs", jobID, map[string]any{"cancel_requested": true, "kind": kind})
	return kind, nil
}

// bulkJobKind reads a bulk_jobs row's kind (RLS-scoped) so the handler can apply per-kind RBAC
// before requesting cancellation. found=false for a missing or cross-Tenant job.
func (s *Service) bulkJobKind(ctx context.Context, id string) (kind string, found bool, err error) {
	err = s.store.Tx(ctx, func(c *pg.Conn) error {
		res, e := c.QueryParams(`select kind from bulk_jobs where id=$1::uuid`, id)
		if e != nil {
			return e
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			kind = s0(res.Rows[0][0])
			found = true
		}
		return nil
	})
	return kind, found, err
}

// cancelAction maps a bulk job kind to the RBAC action that governs cancelling it: cancelling an
// import/key-bulk needs the keys-bulk grant; cancelling a replay needs the queues-replay grant.
func cancelAction(kind string) rbac.Action {
	switch kind {
	case "key_import", "key_bulk":
		return rbac.KeysBulk
	default: // replay, rolling_restart, health checks, benchmarks — operator queue/worker actions
		return rbac.QueuesReplay
	}
}

// CancelRoute mounts POST /v1/admin/bulk-jobs/{id}/cancel on mux, fully wrapped (auth +
// Idempotency-Key + per-kind RBAC + audit). It mirrors BulkJobsRoute: the orchestrator calls it
// with the SAME Service that owns GET /bulk-jobs/{id}, so one durable owner serves both verbs. The
// route path (for openapi-admin) is POST /v1/admin/bulk-jobs/{id}/cancel.
func CancelRoute(mux *http.ServeMux, d Deps, svc *Service) {
	rt := &router{svc: svc, auth: d.Auth, log: d.Logger}
	if rt.log == nil {
		rt.log = slog.Default()
	}
	// Full-literal pattern (not basePath+...): the apispec parity scanner resolves consts per FILE,
	// and basePath is declared in http.go — a literal keeps this route visible to the contract test.
	mux.HandleFunc("POST /v1/admin/bulk-jobs/{id}/cancel", rt.authenticate(rt.requireIdem(rt.cancelBulkJob)))
}

// cancelBulkJob resolves the job kind, enforces per-kind RBAC, then requests cancellation.
func (rt *router) cancelBulkJob(w http.ResponseWriter, r *http.Request) {
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
		return
	}
	id := r.PathValue("id")
	kind, found, err := rt.svc.bulkJobKind(r.Context(), id)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, codeNotFound, "bulk job not found")
		return
	}
	if !rbac.Can(db.RoleFromPrincipal(p), cancelAction(kind)).Allowed() {
		writeError(w, http.StatusForbidden, codeForbidden, "role does not permit cancelling this bulk job")
		return
	}
	if _, err := rt.svc.RequestCancel(r.Context(), id); err != nil {
		rt.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": id, "cancel_requested": true})
}
