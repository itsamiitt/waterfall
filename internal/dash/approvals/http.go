package approvals

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// basePath / historyBase are the mount points for the approvals + change-history surfaces
// (doc 04 §2.12).
const (
	basePath     = "/v1/admin/approvals"
	historyBase  = "/v1/admin/change-history"
	maxBodyBytes = 1 << 20 // 1 MiB (doc 04 §1.1)
)

// Error codes (doc 04 §1.6 registry). approval_required + mfa_required are the approvals-specific
// entries; the rest mirror the shared httpx envelope (writeError is unexported there, so this
// package emits the same shape rather than importing httpx — avoids a cycle).
const (
	codeInvalidJSON      = "invalid_json"
	codeMissingIdemKey   = "missing_idempotency_key"
	codeInvalidCursor    = "invalid_cursor"
	codeInvalidFilter    = "invalid_filter"
	codeUnauthorized     = "unauthorized"
	codeMFARequired      = "mfa_required"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeIdempotencyReuse = "idempotency_key_reuse"
	codeConflict         = "conflict"
	codeApprovalRequired = "approval_required"
	codeValidationFailed = "validation_failed"
	codeInternal         = "internal"
)

// Authenticator resolves a request into a verified Principal (consumer-side; satisfied by
// httpx.CtxAuthenticator). This package never imports httpx.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// Deps bundles the HTTP surface's collaborators. Service is the pre-built engine the orchestrator
// also registered executors on and wired as the Gate; Auth resolves the Principal (behind the
// shared FeatureChain, httpx.CtxAuthenticator); StepUp verifies the per-decision X-MFA-Code.
type Deps struct {
	Service *Service
	Auth    Authenticator
	StepUp  StepUpVerifier // optional; required by contract for approve/reject
	Logger  *slog.Logger
}

type handlers struct {
	svc    *Service
	auth   Authenticator
	stepUp StepUpVerifier
	idem   *idemLedger
	log    *slog.Logger
}

// Routes mounts the approvals + change-history endpoints (doc 04 §2.12) on mux, behind this
// package's authenticate -> RBAC -> (idempotency, writes) -> (step-up, decisions) chain. CSRF / MFA
// / IP-allowlist come from the shared httpx FeatureChain when co-mounted. The orchestrator adds
// "approvals" and "change-history" to the feature-prefix list + featureLabels and wires Deps here.
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: d.Service, auth: d.Auth, stepUp: d.StepUp, idem: newIdemLedger(), log: log}

	mux.HandleFunc("GET "+basePath, h.read(rbac.ApprovalsDecide, h.list))
	mux.HandleFunc("POST "+basePath, h.write(rbac.ApprovalsDecide, h.create))
	mux.HandleFunc("GET "+basePath+"/{id}", h.read(rbac.ApprovalsDecide, h.get))
	mux.HandleFunc("POST "+basePath+"/{id}/approve", h.decide(h.approve))
	mux.HandleFunc("POST "+basePath+"/{id}/reject", h.decide(h.reject))
	mux.HandleFunc("POST "+basePath+"/{id}/cancel", h.write(rbac.ApprovalsDecide, h.cancel))

	// change-history is a read; TA+ (rbac.AuditRead: operator + tenant_admin own-tenant).
	mux.HandleFunc("GET "+historyBase+"/{kind}/{id}", h.read(rbac.AuditRead, h.changeHistory))
}

// --- middleware chain ---

func (h *handlers) read(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, next))
}

func (h *handlers) write(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, h.idempotency(next)))
}

// decide is the approve/reject chain: authenticate -> RBAC(ApprovalsDecide) -> idempotency ->
// step-up (X-MFA-Code). Reaching next means the X-MFA-Code was verified.
func (h *handlers) decide(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(rbac.ApprovalsDecide, h.idempotency(h.requireStepUp(next))))
}

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

// requireStepUp enforces the per-decision X-MFA-Code (doc 05 §5.4). A missing code is always 401; a
// wired verifier that rejects the code is 401. Reaching next proves step-up.
func (h *handlers) requireStepUp(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.Header.Get("X-MFA-Code")
		if code == "" {
			writeError(w, http.StatusUnauthorized, codeMFARequired, "X-MFA-Code is required")
			return
		}
		if h.stepUp != nil && h.stepUp.VerifyStepUp(r.Context(), code) != nil {
			writeError(w, http.StatusUnauthorized, codeMFARequired, "X-MFA-Code missing or invalid")
			return
		}
		next(w, r)
	}
}

// --- read handlers ---

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	cur, err := db.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "cursor is not decodable")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n := atoiOr(v, -1)
		if n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, codeInvalidFilter, "limit must be between 1 and 200")
			return
		}
		limit = n
	}
	status := r.URL.Query().Get("status")
	actionKind := r.URL.Query().Get("action_kind")
	items, next, err := h.svc.ListRequests(r.Context(), status, actionKind, cur, limit)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	if items == nil {
		items = []Request{}
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: items, NextCursor: cursorOut(next)})
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	req, err := h.svc.GetRequest(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "get", err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *handlers) changeHistory(w http.ResponseWriter, r *http.Request) {
	events, err := h.svc.ChangeHistory(r.Context(), r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		h.fail(w, "change-history", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": r.PathValue("kind"), "id": r.PathValue("id"), "events": events})
}

// --- write handlers ---

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActionKind string          `json:"action_kind"`
		Payload    json.RawMessage `json:"payload"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ActionKind == "" || len(body.Payload) == 0 {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "action_kind and payload are required")
		return
	}
	req, err := h.svc.CreateRequest(r.Context(), body.ActionKind, body.Payload)
	if err != nil {
		h.fail(w, "create", err)
		return
	}
	// A gated request is created but not executed inline (doc 04 §5.3): 202 with the request id.
	writeJSON(w, http.StatusAccepted, map[string]any{"approval_request_id": req.ID, "status": req.Status})
}

func (h *handlers) approve(w http.ResponseWriter, r *http.Request) {
	h.decideVerb(w, r, DecisionApprove)
}
func (h *handlers) reject(w http.ResponseWriter, r *http.Request) { h.decideVerb(w, r, DecisionReject) }

func (h *handlers) decideVerb(w http.ResponseWriter, r *http.Request, verb string) {
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
		return
	}
	var body struct {
		Comment string `json:"comment"`
	}
	_ = optionalJSON(r, &body)
	if body.Comment == "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "a justification comment is required")
		return
	}
	role := db.RoleFromPrincipal(p)
	id := r.PathValue("id")
	var req Request
	if verb == DecisionApprove {
		req, err = h.svc.Approve(r.Context(), id, p.UserID, role, body.Comment, true)
	} else {
		req, err = h.svc.Reject(r.Context(), id, p.UserID, role, body.Comment, true)
	}
	if err != nil {
		h.fail(w, verb, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *handlers) cancel(w http.ResponseWriter, r *http.Request) {
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
		return
	}
	req, err := h.svc.Cancel(r.Context(), r.PathValue("id"), p.UserID, db.RoleFromPrincipal(p))
	if err != nil {
		h.fail(w, "cancel", err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// --- error mapping ---

func (h *handlers) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "approval request not found")
	case errors.Is(err, ErrFourEyes):
		writeError(w, http.StatusForbidden, codeForbidden, "requester cannot approve own request")
	case errors.Is(err, ErrApproverRole):
		writeError(w, http.StatusForbidden, codeForbidden, "approver does not hold the required approver role")
	case errors.Is(err, ErrMFARequired):
		writeError(w, http.StatusUnauthorized, codeMFARequired, "X-MFA-Code missing or invalid")
	case errors.Is(err, ErrExpired):
		writeError(w, http.StatusConflict, codeConflict, "request is expired")
	case errors.Is(err, ErrNotPending):
		writeError(w, http.StatusConflict, codeConflict, "request is not pending")
	case errors.Is(err, ErrNoEligibleApprover):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "no eligible approver other than the requester")
	case errors.Is(err, ErrUnknownActionKind):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "unknown action_kind")
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		h.log.Error("approvals handler failed", "op", op, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// --- response helpers + idempotency ledger (doc 04 §1.3; in-process, durable-later like httpx
// D-P0-2) ---

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

func cursorOut(c db.Cursor) *string {
	if len(c.K) == 0 && c.ID == "" {
		return nil
	}
	s := db.EncodeCursor(c)
	return &s
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

func optionalJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return err
	}
	return nil
}

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
