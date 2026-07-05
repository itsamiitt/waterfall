package rotation

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// HTTP surface for module 4 (doc 04 §2.5): the 5 Rotation endpoints under /v1/admin. They are
// operator-only (Class P platform surface): selection-state / strategies / simulate are debug reads
// and triggers GET/PUT are operator knobs, all gated by rbac.RotationWrite (operator-allow, others
// deny). This package never imports internal/dash/httpx, so the surfaces stay decoupled; the shared
// FeatureChain supplies authentication + CSRF + MFA-gate when co-mounted.

const (
	basePath     = "/v1/admin"
	maxBodyBytes = 1 << 20
)

// Error codes (doc 04 §1.6 registry subset).
const (
	codeInvalidJSON    = "invalid_json"
	codeMissingIdemKey = "missing_idempotency_key"
	codeUnauthorized   = "unauthorized"
	codeForbidden      = "forbidden"
	codeNotFound       = "not_found"
	codeConflict       = "conflict"
	codeValidation     = "validation_failed"
	codeInternal       = "internal"
)

// Authenticator resolves a request into a verified Principal (consumer-side; satisfied by
// *httpx.SessionOrJWT / httpx.CtxAuthenticator).
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// Deps are the constructed dependencies Routes needs. Engine is the SHARED rotation engine (the
// same instance wired as providers.Deps.Resolver), so selection-state reflects the live cache the
// egress path uses.
type Deps struct {
	Engine *Engine
	Auth   Authenticator
	Logger *slog.Logger
}

type handlers struct {
	eng  *Engine
	auth Authenticator
	log  *slog.Logger
}

// Routes mounts the 5 Rotation endpoints on mux. The key-pools/{id}/selection-state and
// key-pools/{id}/simulate patterns coexist with the internal/dash/keys key-pools routes on the SAME
// mux — net/http 1.22 disambiguates by full pattern, so there is no duplicate-registration panic.
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{eng: d.Engine, auth: d.Auth, log: log}
	mux.HandleFunc("GET "+basePath+"/rotation/strategies", h.read(h.strategies))
	mux.HandleFunc("GET "+basePath+"/rotation/triggers", h.read(h.listTriggers))
	mux.HandleFunc("PUT "+basePath+"/rotation/triggers", h.write(h.putTrigger))
	mux.HandleFunc("GET "+basePath+"/key-pools/{id}/selection-state", h.read(h.selectionState))
	mux.HandleFunc("POST "+basePath+"/key-pools/{id}/simulate", h.read(h.simulate))
}

// --- middleware ---

// read authenticates + RBAC-guards (operator via rotation.write decision) a read/dry-run handler.
func (h *handlers) read(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireOperator(next))
}

// write additionally enforces the Idempotency-Key header (doc 04 §1.3).
func (h *handlers) write(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireOperator(h.requireIdem(next)))
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

// requireOperator gates the rotation surface: rbac.RotationWrite is operator-allow / others-deny, so
// this enforces operator-only access for every rotation endpoint (Class P operator surface).
func (h *handlers) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := tenant.FromContext(r.Context())
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
			return
		}
		if !rbac.Can(db.RoleFromPrincipal(p), rbac.RotationWrite).Allowed() {
			writeError(w, http.StatusForbidden, codeForbidden, "operator role required")
			return
		}
		next(w, r)
	}
}

func (h *handlers) requireIdem(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("Idempotency-Key"); k == "" || len(k) > 128 {
			writeError(w, http.StatusBadRequest, codeMissingIdemKey, "the Idempotency-Key header is required on writes")
			return
		}
		next(w, r)
	}
}

// --- handlers ---

func (h *handlers) strategies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"strategies": Strategies})
}

func (h *handlers) listTriggers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.eng.ListTriggers(r.Context())
	if err != nil {
		h.fail(w, "list-triggers", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggers": rows})
}

func (h *handlers) putTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Trigger    string          `json:"trigger"`
		Thresholds json.RawMessage `json:"thresholds"`
		CooldownS  *int64          `json:"cooldown_s"`
		Enabled    *bool           `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tr := TriggerRow{
		Trigger:   req.Trigger,
		CooldownS: req.CooldownS,
		Enabled:   enabled,
	}
	if len(req.Thresholds) > 0 {
		tr.Thresholds = string(req.Thresholds)
	}
	out, err := h.eng.PutTrigger(r.Context(), tr)
	if err != nil {
		h.fail(w, "put-trigger", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) selectionState(w http.ResponseWriter, r *http.Request) {
	snap, err := h.eng.SelectionState(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "selection-state", err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h *handlers) simulate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		N      int    `json:"n"`
		Region string `json:"region"`
	}
	_ = optionalJSON(r, &req)
	res, err := h.eng.Simulate(r.Context(), r.PathValue("id"), req.N, req.Region)
	if err != nil {
		h.fail(w, "simulate", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- error mapping + helpers ---

func (h *handlers) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, ErrPoolNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "key pool not found")
	case errors.Is(err, ErrNoKeyAvailable):
		writeError(w, http.StatusConflict, codeConflict, "no available key in pool")
	case errors.Is(err, ErrIllegalTransition):
		writeError(w, http.StatusConflict, codeConflict, "illegal key state transition")
	case errors.Is(err, ErrInvalidTrigger), errors.Is(err, ErrAuthTriggerImmutable):
		writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error())
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		h.log.Error("rotation handler failed", "op", op, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
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
