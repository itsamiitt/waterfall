package crm

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// crmBase is the CRM dashboard route prefix (docs/research-intelligence/08, ADR-0030).
const crmBase = "/v1/admin/crm"

const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeInternal     = "internal"
)

// Authenticator resolves a request into a verified Principal (satisfied by httpx.CtxAuthenticator).
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// Deps bundles the CRM-dashboard surface's collaborators.
type Deps struct {
	Service *Service
	Auth    Authenticator
	Logger  *slog.Logger
}

type handlers struct {
	svc  *Service
	auth Authenticator
	log  *slog.Logger
}

// Routes mounts the CRM connection read endpoints under /v1/admin/crm. Reads: the shared FeatureChain
// supplies session auth (CSRF exempt for GET). RBAC (CRMRead) + RLS scope the rows to the Tenant.
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: d.Service, auth: d.Auth, log: log}
	mux.HandleFunc("GET "+crmBase+"/connections", h.read(rbac.CRMRead, h.list))
	mux.HandleFunc("GET "+crmBase+"/connections/{id}", h.read(rbac.CRMRead, h.connection))
}

func (h *handlers) read(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, next))
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

// list handles GET /v1/admin/crm/connections — the Tenant's configured CRM connections.
func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	items, err := h.svc.List(r.Context(), limit)
	if err != nil {
		h.log.Error("crm dashboard list failed", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// connection handles GET /v1/admin/crm/connections/{id} — one CRM connection.
func (h *handlers) connection(w http.ResponseWriter, r *http.Request) {
	c, ok, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.log.Error("crm dashboard get failed", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "no connection with this id")
		return
	}
	writeJSON(w, http.StatusOK, c)
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
