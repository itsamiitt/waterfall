package airouting

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// aiBase is the AI routing/models dashboard route prefix (docs/research-intelligence/08).
const aiBase = "/v1/admin/ai"

const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
)

// Authenticator resolves a request into a verified Principal (satisfied by httpx.CtxAuthenticator).
// This package never imports httpx, so it declares the consumer-side interface.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// Deps bundles the AI-models surface's collaborators.
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

// Routes mounts the AI models catalog under /v1/admin/ai. Read-only and operator-only (the LLM
// registry is platform config, not tenant data — RBAC AIModelsRead is operator-allow, TA/TU deny).
// There is no RLS transaction here: the projection is identical for every caller.
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: d.Service, auth: d.Auth, log: log}
	mux.HandleFunc("GET "+aiBase+"/models", h.read(rbac.AIModelsRead, h.models))
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

// models handles GET /v1/admin/ai/models — the platform LLM cascade catalog (free-first order).
func (h *handlers) models(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": h.svc.Models()})
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
