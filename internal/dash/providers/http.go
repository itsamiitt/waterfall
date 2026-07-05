package providers

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/approvals"
	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

// basePath is the mount point for the Provider Management surface (doc 04 §2.3).
const basePath = "/v1/admin/providers"

const maxBodyBytes = 1 << 20 // 1 MiB (doc 04 §1.1)

// Error codes (doc 04 §1.6 registry; identical envelope to internal/dash/httpx). httpx.writeError
// is unexported, so this package emits the same shape rather than importing it (avoids an httpx
// dependency and a would-be cycle).
const (
	codeInvalidJSON      = "invalid_json"
	codeMissingIdemKey   = "missing_idempotency_key"
	codeInvalidCursor    = "invalid_cursor"
	codeInvalidFilter    = "invalid_filter"
	codeUnauthorized     = "unauthorized"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeIdempotencyReuse = "idempotency_key_reuse"
	codeConflict         = "conflict"
	codeValidationFailed = "validation_failed"
	codeInternal         = "internal"
)

// Authenticator resolves a request into a verified Principal (consumer-side; satisfied by
// *httpx.SessionOrJWT). Kept as an interface so this package never imports httpx.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// Deps bundles the collaborators Routes needs. Only Store, Audit, and Auth are required; Secrets
// and Resolver enable the credentialed probe actions (test/health-check/benchmark/sync-credits).
type Deps struct {
	Store    *db.Store
	Audit    *audit.Log
	Auth     Authenticator
	Secrets  secrets.Backend      // optional (reserved; provider ops hold no secrets directly)
	Resolver provider.KeyResolver // optional; nil => probe actions report a typed no-key result
	Gate     approvals.Gate       // optional; nil => approvals.NopGate (destructive actions run inline)
	Now      func() time.Time
	Logger   *slog.Logger
}

// NewService builds the Provider Management service from Deps (PGStore over the Class-P table).
func NewService(d Deps) *Service {
	return newService(NewPGStore(d.Store), d.Audit, d.Resolver, d.Now)
}

// handlers is the HTTP adapter around a Service.
type handlers struct {
	svc  *Service
	auth Authenticator
	gate approvals.Gate
	idem *idemLedger
	log  *slog.Logger
}

// Routes registers the 21 Provider Management endpoints (doc 04 §2.3) on mux, each wrapped in the
// package's own authenticate -> RBAC -> (idempotency, writes only) chain. Auditing is performed
// inside the Service (it owns the before/after images). CSRF / MFA / IP-allowlist are applied by
// the shared httpx chain when co-mounted; this package enforces the guarantees specific to the
// provider surface (identity, authorization, idempotency, audit).
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	gate := d.Gate
	if gate == nil {
		gate = approvals.NopGate{}
	}
	registerRoutes(mux, &handlers{svc: NewService(d), auth: d.Auth, gate: gate, idem: newIdemLedger(), log: log})
}

// registerRoutes mounts the endpoint table for h (split out so tests can wire a fake-backed
// handlers without a live db.Store).
func registerRoutes(mux *http.ServeMux, h *handlers) {
	// Collection + aggregates (literal segments beat the {id} wildcard in net/http 1.22).
	mux.HandleFunc("GET "+basePath, h.read(rbac.ProvidersRead, h.list))
	mux.HandleFunc("POST "+basePath, h.write(rbac.ProvidersWrite, h.create))
	mux.HandleFunc("GET "+basePath+"/compare", h.readOp(rbac.ProvidersRead, h.compare))
	mux.HandleFunc("GET "+basePath+"/rankings", h.readOp(rbac.ProvidersRead, h.rankings))
	mux.HandleFunc("GET "+basePath+"/coverage", h.read(rbac.ProvidersRead, h.coverage))

	// Item.
	mux.HandleFunc("GET "+basePath+"/{id}", h.read(rbac.ProvidersRead, h.get))
	mux.HandleFunc("PATCH "+basePath+"/{id}", h.write(rbac.ProvidersWrite, h.patch))
	mux.HandleFunc("DELETE "+basePath+"/{id}", h.write(rbac.ProvidersDelete, h.delete))

	// op_state lifecycle actions.
	for _, action := range []string{"enable", "disable", "pause", "maintenance"} {
		mux.HandleFunc("POST "+basePath+"/{id}/"+action, h.write(rbac.ProvidersWrite, h.opAction(action)))
	}

	// Operational actions.
	mux.HandleFunc("POST "+basePath+"/{id}/test", h.write(rbac.ProvidersWrite, h.test))
	mux.HandleFunc("POST "+basePath+"/{id}/health-check", h.write(rbac.ProvidersWrite, h.healthCheck))
	mux.HandleFunc("POST "+basePath+"/{id}/refresh-metadata", h.write(rbac.ProvidersWrite, h.refreshMetadata))
	mux.HandleFunc("POST "+basePath+"/{id}/sync-credits", h.write(rbac.ProvidersWrite, h.syncCredits))
	mux.HandleFunc("POST "+basePath+"/{id}/benchmark", h.write(rbac.ProvidersWrite, h.benchmark))
	mux.HandleFunc("POST "+basePath+"/{id}/duplicate", h.write(rbac.ProvidersWrite, h.duplicate))
	mux.HandleFunc("POST "+basePath+"/{id}/archive", h.write(rbac.ProvidersDelete, h.archive))

	// Item reads (operator scope).
	mux.HandleFunc("GET "+basePath+"/{id}/health", h.readOp(rbac.ProvidersRead, h.health))
	mux.HandleFunc("GET "+basePath+"/{id}/stats", h.readOp(rbac.ProvidersRead, h.stats))
}

// --- middleware chain ---

func (h *handlers) read(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, next))
}

func (h *handlers) readOp(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, h.requireOperator(next)))
}

func (h *handlers) write(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, h.idempotency(next)))
}

// authenticate binds the verified Principal into ctx (G1). It is a no-op when an upstream chain
// already bound one, so this surface composes with or without an outer authenticator.
func (h *handlers) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, err := tenant.FromContext(ctx); err != nil {
			if h.auth == nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			p, err := h.auth.Authenticate(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			ctx = tenant.WithPrincipal(ctx, p)
		}
		next(w, r.WithContext(ctx))
	}
}

// requireRole is the coarse RBAC guard (doc 05 §2). Deny => 403 forbidden.
func (h *handlers) requireRole(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := tenant.FromContext(r.Context())
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
			return
		}
		if !rbac.Can(db.RoleFromPrincipal(p), action).Allowed() {
			writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
			return
		}
		next(w, r)
	}
}

// requireOperator refines a ProvidersRead route to operator-only (doc 05 §2: provider
// health/stats/compare/rankings are operator-only; tenants get only the catalog projection).
func (h *handlers) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		if db.RoleFromPrincipal(p) != rbac.RoleOperator {
			writeError(w, http.StatusForbidden, codeForbidden, "operator role required")
			return
		}
		next(w, r)
	}
}

// --- read handlers ---

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	items, next, err := h.svc.List(r.Context(), parseFilter(r), cur, limit)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	out := make([]providerDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toDTO(p))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: out, NextCursor: cursorOut(encodeCursor(next))})
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "get", err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(p))
}

func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "health", err)
		return
	}
	av := effectiveOf(p)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                  p.ID,
		"effective_available": av.Available(),
		"availability":        string(av.State),
		"unavailable_reason":  reasonOut(av.Reason),
		"health_score":        p.HealthScore,
		"avg_latency_ms":      p.AvgLatencyMS,
		"last_health_at":      p.LastHealthAt,
		"last_success_at":     p.LastSuccessAt,
		"last_failure_at":     p.LastFailureAt,
	})
}

// stats is a P1 stub: the provider_stats_* rollups land with the observability module (doc 10),
// so this returns an empty, well-formed series rather than referencing a table that does not yet
// exist. Documented in the final report.
func (h *handlers) stats(w http.ResponseWriter, r *http.Request) {
	if _, err := h.svc.Get(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, "stats", err)
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     r.PathValue("id"),
		"res":    q.Get("res"),
		"from":   q.Get("from"),
		"to":     q.Get("to"),
		"series": []any{},
		"note":   "provider_stats_* rollups arrive with the observability module (P1 stub)",
	})
}

func (h *handlers) compare(w http.ResponseWriter, r *http.Request) {
	ids := splitIDs(r.URL.Query().Get("ids"))
	if len(ids) == 0 || len(ids) > 10 {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "ids must list 1..10 provider ids")
		return
	}
	entries, err := h.svc.Compare(r.Context(), ids)
	if err != nil {
		h.fail(w, "compare", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func (h *handlers) rankings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Rankings(r.Context(), r.URL.Query().Get("metric"))
	if err != nil {
		h.fail(w, "rankings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (h *handlers) coverage(w http.ResponseWriter, r *http.Request) {
	rep, err := h.svc.Coverage(r.Context())
	if err != nil {
		h.fail(w, "coverage", err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// --- write handlers ---

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	var req providerWriteReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ID == nil || strings.TrimSpace(*req.ID) == "" || req.DisplayName == nil || *req.DisplayName == "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "id and display_name are required")
		return
	}
	if msg, ok := req.validate(); !ok {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, msg)
		return
	}
	var b colsBuilder
	b.strForce("id", *req.ID)
	req.apply(&b)
	// Defaults (ADR-0009): new providers are DEPRIORITIZED + pending review + disabled.
	if req.Status == nil {
		b.strForce("status", StatusDeprioritized)
	}
	if req.ComplianceReviewStatus == nil {
		b.strForce("compliance_review_status", "pending")
	}
	b.strForce("op_state", OpDisabled)
	if req.Visibility == nil {
		b.strForce("visibility", VisibilityTenantReadable)
	}
	p, err := h.svc.Create(r.Context(), b.cv)
	if err != nil {
		h.fail(w, "create", err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(p))
}

func (h *handlers) patch(w http.ResponseWriter, r *http.Request) {
	var req providerWriteReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ID != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "id is immutable")
		return
	}
	if msg, ok := req.validate(); !ok {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, msg)
		return
	}
	var b colsBuilder
	req.apply(&b)
	if len(b.cv) == 0 {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "no updatable fields supplied")
		return
	}
	p, err := h.svc.Patch(r.Context(), r.PathValue("id"), b.cv)
	if err != nil {
		h.fail(w, "patch", err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(p))
}

// delete is approval-gated (doc 04 §2.3; OI-IP-2 RESOLVED in P4). It pins the {"id":...} payload
// into an approval request via the Gate; on a pending decision it returns 202 {approval_request_id}
// instead of deleting. The real delete then runs EXACTLY ONCE through the registered Executor on
// quorum. proceed=true (a defensively-disarmed policy) falls through to the inline delete.
func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if reqID, gated, ok := h.checkGate(w, r, approvals.ActionProviderDelete, gatePayload("id", id)); !ok {
		return
	} else if gated {
		writeJSON(w, http.StatusAccepted, map[string]string{"approval_request_id": reqID})
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		h.fail(w, "delete", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

func (h *handlers) archive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body actionBody
	_ = optionalJSON(r, &body)
	if reqID, gated, ok := h.checkGate(w, r, approvals.ActionProviderArchive, gatePayload("id", id)); !ok {
		return
	} else if gated {
		writeJSON(w, http.StatusAccepted, map[string]string{"approval_request_id": reqID})
		return
	}
	p, err := h.svc.Archive(r.Context(), id)
	if err != nil {
		h.fail(w, "archive", err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(p))
}

// checkGate asks the approval Gate whether actionKind may proceed inline for the pinned payload.
// It returns (requestID, gated, ok): ok=false means an error response was already written (500);
// gated=true means the caller must answer 202 {approval_request_id}; gated=false means proceed
// inline. The NopGate always returns (,"" , false, true) so a nil-Gate deployment is unchanged.
func (h *handlers) checkGate(w http.ResponseWriter, r *http.Request, actionKind string, payload []byte) (string, bool, bool) {
	if h.gate == nil {
		return "", false, true // no gate wired => proceed inline (unchanged behavior)
	}
	proceed, reqID, err := h.gate.Check(r.Context(), actionKind, payload)
	if err != nil {
		h.log.Error("approval gate check failed", "action_kind", actionKind, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "approval gate error")
		return "", false, false
	}
	return reqID, !proceed, true
}

// gatePayload marshals a single-key pinned payload, e.g. {"id":"acme"}.
func gatePayload(key, val string) []byte {
	b, _ := json.Marshal(map[string]string{key: val})
	return b
}

func (h *handlers) opAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body actionBody
		_ = optionalJSON(r, &body)
		p, err := h.svc.SetOpState(r.Context(), r.PathValue("id"), action, body.Reason)
		if err != nil {
			h.fail(w, action, err)
			return
		}
		writeJSON(w, http.StatusOK, toDTO(p))
	}
}

func (h *handlers) test(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Test(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "test", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "result": res})
}

func (h *handlers) healthCheck(w http.ResponseWriter, r *http.Request) {
	p, res, err := h.svc.HealthCheck(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "health-check", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": p.ID, "result": res, "last_health_at": p.LastHealthAt})
}

func (h *handlers) refreshMetadata(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.RefreshMetadata(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "refresh-metadata", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": p.ID, "last_sync_at": p.LastSyncAt, "note": "P1 stub"})
}

func (h *handlers) syncCredits(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CreditsRemaining *int64 `json:"credits_remaining"`
	}
	_ = optionalJSON(r, &body)
	p, res, err := h.svc.SyncCredits(r.Context(), r.PathValue("id"), body.CreditsRemaining)
	if err != nil {
		h.fail(w, "sync-credits", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": p.ID, "credits_remaining": p.CreditsRemaining, "last_sync_at": p.LastSyncAt, "result": res,
	})
}

func (h *handlers) benchmark(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Benchmark(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "benchmark", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "benchmark": res})
}

func (h *handlers) duplicate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	_ = optionalJSON(r, &body)
	srcID := r.PathValue("id")
	newID := strings.TrimSpace(body.ID)
	if newID == "" {
		newID = srcID + "-copy"
	}
	p, err := h.svc.Duplicate(r.Context(), srcID, newID, body.DisplayName)
	if err != nil {
		h.fail(w, "duplicate", err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(p))
}

// fail maps a service error to a uniform status without disclosing cross-scope existence.
func (h *handlers) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "provider not found")
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, codeConflict, "provider id already exists")
	case errors.Is(err, ErrInvalidTransition):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "invalid op_state transition")
	case errors.Is(err, ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "validation failed")
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		h.log.Error("provider handler failed", "op", op, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// --- idempotency (doc 04 §1.3) ---
// httpx's idempotency middleware is unexported, so this surface carries an equivalent in-process
// ledger: writes require an Idempotency-Key; a repeat with the SAME body replays the stored
// response; a repeat with a DIFFERENT body is 409 (Deviation, like httpx D-P0-2: durable ledger later).

type idemLedger struct {
	mu      sync.Mutex
	entries map[string]*idemEntry
}

type idemEntry struct {
	hash   [32]byte
	status int
	body   []byte
	done   bool
}

func newIdemLedger() *idemLedger { return &idemLedger{entries: map[string]*idemEntry{}} }

func (h *handlers) idempotency(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 128 {
			writeError(w, http.StatusBadRequest, codeMissingIdemKey, "the Idempotency-Key header is required on writes")
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		r.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(body)

		p, _ := tenant.FromContext(r.Context())
		lk := p.TenantID + "\x00" + key

		h.idem.mu.Lock()
		e, seen := h.idem.entries[lk]
		if seen {
			if subtle.ConstantTimeCompare(e.hash[:], sum[:]) != 1 {
				h.idem.mu.Unlock()
				writeError(w, http.StatusConflict, codeIdempotencyReuse, "Idempotency-Key was reused with a different request body")
				return
			}
			if e.done {
				status, respBody := e.status, e.body
				h.idem.mu.Unlock()
				w.Header().Set("Idempotency-Replayed", "true")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(respBody)
				return
			}
			h.idem.mu.Unlock() // in-flight duplicate: let it proceed
			next(w, r)
			return
		}
		e = &idemEntry{hash: sum}
		h.idem.entries[lk] = e
		h.idem.mu.Unlock()

		cw := &captureWriter{header: w.Header().Clone(), status: http.StatusOK}
		next(cw, r)

		h.idem.mu.Lock()
		e.status, e.body, e.done = cw.status, cw.buf.Bytes(), true
		h.idem.mu.Unlock()

		for k, vs := range cw.header {
			for _, v := range vs {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(cw.status)
		_, _ = w.Write(cw.buf.Bytes())
	}
}

type captureWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) WriteHeader(code int)        { c.status = code }
func (c *captureWriter) Write(b []byte) (int, error) { return c.buf.Write(b) }

// --- response helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

type listEnvelope struct {
	Items      any     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func cursorOut(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func reasonOut(reason string) *string {
	if reason == "" {
		return nil
	}
	return &reason
}
