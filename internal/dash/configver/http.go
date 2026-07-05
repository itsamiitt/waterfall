package configver

import (
	"bytes"
	"context"
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

const maxBodyBytes = 1 << 20 // 1 MiB (doc 04 §1.1)

// Error codes (doc 04 §1.6 registry subset). version_conflict + validation_failed reconcile the
// doc 12 P3 gate; invalid_scope_key is doc 07 §3.1.
const (
	codeInvalidJSON      = "invalid_json"
	codeMissingIdemKey   = "missing_idempotency_key"
	codeInvalidCursor    = "invalid_cursor"
	codeInvalidFilter    = "invalid_filter"
	codeInvalidScopeKey  = "invalid_scope_key"
	codeUnauthorized     = "unauthorized"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeIdempotencyReuse = "idempotency_key_reuse"
	codeVersionConflict  = "version_conflict"
	codeValidationFailed = "validation_failed"
	codeInternal         = "internal"
)

// Authenticator resolves a request into a verified Principal (consumer-side; satisfied by
// httpx.CtxAuthenticator). This package never imports httpx.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// DryRunRequest is the optional dry-run body: the Fields to plan for and a request region for
// scope/region-aware resolution. Absent Want falls to a kind-chosen default.
type DryRunRequest struct {
	Want   []string `json:"want"`
	Region string   `json:"region"`
}

// DryRunner runs the read-only, ZERO-egress Provider simulator for a kind (doc 07 §7): the real
// router.Planner against the draft payload with current reservation values. Injected per kind by
// routing / workflows.
type DryRunner interface {
	DryRun(ctx context.Context, scopeKey string, payload json.RawMessage, req DryRunRequest) (any, error)
}

// KindSpec parameterizes the generic HTTP surface for one config kind.
type KindSpec struct {
	Kind        string      // e.g. KindRoutingPolicy
	BasePath    string      // e.g. "/v1/admin/routing"
	RBACAction  rbac.Action // gates the surface (routing.publish / workflows.publish)
	DryRun      DryRunner   // per-kind dry-run
	IndexAsList bool        // true => GET {BasePath} serves the workflow_index instead of config_active
}

// HTTPDeps bundles the collaborators the generic handlers need.
type HTTPDeps struct {
	Service *Service
	Auth    Authenticator
	Logger  *slog.Logger
}

type handlers struct {
	svc  *Service
	spec KindSpec
	auth Authenticator
	idem *idemLedger
	log  *slog.Logger
}

// Mount registers the per-kind config lifecycle endpoints on mux at spec.BasePath (doc 04
// Routing+Workflows). routing / workflows call this then add their kind-specific extras
// (GET config/epochs, GET workflows index). Distinct full patterns mean routing and workflows can
// share one mux with no duplicate-registration panic.
func Mount(mux *http.ServeMux, spec KindSpec, d HTTPDeps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: d.Service, spec: spec, auth: d.Auth, idem: newIdemLedger(), log: log}
	bp := spec.BasePath
	mux.HandleFunc("GET "+bp, h.read(h.list))
	mux.HandleFunc("GET "+bp+"/{scope}/versions", h.read(h.listVersions))
	mux.HandleFunc("POST "+bp+"/{scope}/versions", h.write(h.createDraft))
	mux.HandleFunc("GET "+bp+"/{scope}/versions/{id}", h.read(h.getVersion))
	mux.HandleFunc("PATCH "+bp+"/{scope}/versions/{id}", h.write(h.patchDraft))
	mux.HandleFunc("POST "+bp+"/{scope}/versions/{id}/validate", h.write(h.validate))
	mux.HandleFunc("POST "+bp+"/{scope}/versions/{id}/publish", h.write(h.publish))
	mux.HandleFunc("POST "+bp+"/{scope}/versions/{id}/dry-run", h.write(h.dryRun))
	mux.HandleFunc("POST "+bp+"/{scope}/versions/{id}/clone", h.write(h.clone))
	mux.HandleFunc("POST "+bp+"/{scope}/rollback", h.write(h.rollback))
}

// MountEpochs registers GET /v1/admin/config/epochs (the cheap epoch poll target, doc 07 §10).
func MountEpochs(mux *http.ServeMux, d HTTPDeps) {
	h := &handlers{svc: d.Service, auth: d.Auth, log: orDefault(d.Logger)}
	mux.HandleFunc("GET /v1/admin/config/epochs", h.read(h.epochs))
}

func orDefault(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}

// --- middleware ---

func (h *handlers) read(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRBAC(next))
}

func (h *handlers) write(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRBAC(h.idempotency(next)))
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

// requireRBAC gates the surface by the kind's publish action (operator + tenant_admin allowed,
// tenant_user denied). The action is DecisionApprovalGated for the config kinds; Allowed() is true
// for approval-gated, so it admits the same roles for reads and non-publish writes.
func (h *handlers) requireRBAC(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := tenant.FromContext(r.Context())
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
			return
		}
		if !rbac.Can(db.RoleFromPrincipal(p), h.spec.RBACAction).Allowed() {
			writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
			return
		}
		next(w, r)
	}
}

// --- read handlers ---

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	if h.spec.IndexAsList {
		h.workflowIndex(w, r)
		return
	}
	cur, limit, ok := h.page(w, r)
	if !ok {
		return
	}
	items, next, err := h.svc.store.ListActive(r.Context(), h.spec.Kind, cur, limit)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: nonNil(items), NextCursor: cursorOut(next)})
}

func (h *handlers) listVersions(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	cur, limit, ok := h.page(w, r)
	if !ok {
		return
	}
	items, next, err := h.svc.ListVersions(r.Context(), h.spec.Kind, scope, cur, limit)
	if err != nil {
		h.fail(w, "list-versions", err)
		return
	}
	out := make([]versionDTO, 0, len(items))
	for _, v := range items {
		out = append(out, toDTO(v))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: out, NextCursor: cursorOut(next)})
}

func (h *handlers) getVersion(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.GetVersion(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "get", err)
		return
	}
	if v.Kind != h.spec.Kind || v.ScopeKey != r.PathValue("scope") {
		writeError(w, http.StatusNotFound, codeNotFound, "version not found")
		return
	}
	writeJSON(w, http.StatusOK, toDTO(v))
}

func (h *handlers) epochs(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Epochs(r.Context())
	if err != nil {
		h.fail(w, "epochs", err)
		return
	}
	if rows == nil {
		rows = []Epoch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"epochs": rows})
}

func (h *handlers) workflowIndex(w http.ResponseWriter, r *http.Request) {
	cur, limit, ok := h.page(w, r)
	if !ok {
		return
	}
	rows, next, err := h.svc.ListWorkflows(r.Context(), cur, limit)
	if err != nil {
		h.fail(w, "workflows", err)
		return
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: nonNilWF(rows), NextCursor: cursorOut(next)})
}

// --- write handlers ---

func (h *handlers) createDraft(w http.ResponseWriter, r *http.Request) {
	payload, ok := readPayload(w, r)
	if !ok {
		return
	}
	v, err := h.svc.CreateDraft(r.Context(), h.spec.Kind, r.PathValue("scope"), payload)
	if err != nil {
		h.fail(w, "create-draft", err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(v))
}

func (h *handlers) patchDraft(w http.ResponseWriter, r *http.Request) {
	payload, ok := readPayload(w, r)
	if !ok {
		return
	}
	if !h.ownScope(w, r) {
		return
	}
	v, err := h.svc.PatchDraft(r.Context(), r.PathValue("id"), payload)
	if err != nil {
		h.fail(w, "patch-draft", err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(v))
}

func (h *handlers) validate(w http.ResponseWriter, r *http.Request) {
	if !h.ownScope(w, r) {
		return
	}
	v, err := h.svc.Validate(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "validate", err)
		return
	}
	// Validation always returns 200; a failed rule is report content (doc 07 §5).
	writeJSON(w, http.StatusOK, map[string]any{
		"id": v.ID, "status": v.Status, "validation_report": v.ValidationReport,
	})
}

func (h *handlers) publish(w http.ResponseWriter, r *http.Request) {
	if !h.ownScope(w, r) {
		return
	}
	expected := expectedParam(r)
	v, err := h.svc.Publish(r.Context(), r.PathValue("id"), expected)
	if err != nil {
		h.fail(w, "publish", err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(v))
}

func (h *handlers) rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ToVersion               int     `json:"to_version"`
		ExpectedActiveVersionID *string `json:"expected_active_version_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ToVersion < 1 {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "to_version must be a positive version number")
		return
	}
	v, err := h.svc.Rollback(r.Context(), h.spec.Kind, r.PathValue("scope"), body.ToVersion, body.ExpectedActiveVersionID)
	if err != nil {
		h.fail(w, "rollback", err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(v))
}

func (h *handlers) dryRun(w http.ResponseWriter, r *http.Request) {
	if h.spec.DryRun == nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "dry-run not configured")
		return
	}
	if !h.ownScope(w, r) {
		return
	}
	v, err := h.svc.GetVersion(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "dry-run", err)
		return
	}
	var req DryRunRequest
	_ = optionalJSON(r, &req)
	out, err := h.spec.DryRun.DryRun(r.Context(), v.ScopeKey, v.Payload, req)
	if err != nil {
		h.fail(w, "dry-run", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) clone(w http.ResponseWriter, r *http.Request) {
	if !h.ownScope(w, r) {
		return
	}
	v, err := h.svc.Clone(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "clone", err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(v))
}

// ownScope guards that the {id} version belongs to this kind + the {scope} in the path (404 on
// mismatch, so a cross-scope id is never disclosed).
func (h *handlers) ownScope(w http.ResponseWriter, r *http.Request) bool {
	v, err := h.svc.GetVersion(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "scope-check", err)
		return false
	}
	if v.Kind != h.spec.Kind || v.ScopeKey != r.PathValue("scope") {
		writeError(w, http.StatusNotFound, codeNotFound, "version not found")
		return false
	}
	return true
}

func (h *handlers) page(w http.ResponseWriter, r *http.Request) (db.Cursor, int, bool) {
	cur, err := db.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "cursor is not decodable")
		return db.Cursor{}, 0, false
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n := atoiSafe(v)
		if n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, codeInvalidFilter, "limit must be between 1 and 200")
			return db.Cursor{}, 0, false
		}
		limit = n
	}
	return cur, limit, true
}

// --- error mapping ---

func (h *handlers) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "version not found")
	case errors.Is(err, ErrVersionConflict):
		writeError(w, http.StatusConflict, codeVersionConflict, "version conflict: the active pointer moved or the version is immutable")
	case errors.Is(err, ErrNotValidated):
		writeError(w, http.StatusConflict, codeVersionConflict, "version is not validated")
	case errors.Is(err, ErrHashMismatch):
		writeError(w, http.StatusConflict, codeVersionConflict, "payload hash mismatch")
	case errors.Is(err, ErrInvalidScopeKey):
		writeError(w, http.StatusBadRequest, codeInvalidScopeKey, "scope_key is malformed")
	case errors.Is(err, ErrInvalidPayload):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "payload must be a JSON object")
	case errors.Is(err, ErrNoValidator):
		writeError(w, http.StatusInternalServerError, codeInternal, "no validator configured for kind")
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		h.log.Error("configver handler failed", "op", op, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// --- request/response helpers ---

func readPayload(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	var body struct {
		Payload json.RawMessage `json:"payload"`
	}
	if !decodeJSON(w, r, &body) {
		return nil, false
	}
	if len(body.Payload) == 0 {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "a payload object is required")
		return nil, false
	}
	return body.Payload, true
}

// expectedParam reads expected_active_version_id from the request body (optional). Absent -> nil
// (the Service defaults to the version's parent_version_id, doc 07 §6).
func expectedParam(r *http.Request) *string {
	var body struct {
		ExpectedActiveVersionID *string `json:"expected_active_version_id"`
	}
	_ = optionalJSON(r, &body)
	return body.ExpectedActiveVersionID
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

func nonNil(items []ActiveEntry) []ActiveEntry {
	if items == nil {
		return []ActiveEntry{}
	}
	return items
}

func nonNilWF(items []WorkflowRow) []WorkflowRow {
	if items == nil {
		return []WorkflowRow{}
	}
	return items
}

func atoiSafe(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
		if n > 1<<30 {
			return 1 << 30
		}
	}
	if s == "" {
		return -1
	}
	return n
}

// --- version DTO ---

type versionDTO struct {
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	ScopeKey         string          `json:"scope_key"`
	Version          int             `json:"version"`
	Status           string          `json:"status"`
	Payload          json.RawMessage `json:"payload"`
	ValidationReport json.RawMessage `json:"validation_report,omitempty"`
	ParentVersionID  string          `json:"parent_version_id,omitempty"`
	CreatedAt        string          `json:"created_at,omitempty"`
	PublishedAt      string          `json:"published_at,omitempty"`
}

func toDTO(v Version) versionDTO {
	d := versionDTO{
		ID: v.ID, Kind: v.Kind, ScopeKey: v.ScopeKey, Version: v.Version, Status: v.Status,
		Payload: v.Payload, ValidationReport: v.ValidationReport, ParentVersionID: v.ParentVersionID,
	}
	if !v.CreatedAt.IsZero() {
		d.CreatedAt = v.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if v.PublishedAt != nil {
		d.PublishedAt = v.PublishedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return d
}

// --- idempotency ledger (doc 04 §1.3): writes require an Idempotency-Key; same key + same body
// replays the stored response; same key + different body -> 409 (in-process; durable ledger later,
// like the providers/keys surfaces). ---

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
			h.idem.mu.Unlock()
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
