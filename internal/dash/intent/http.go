package intent

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// intentBase is the Intent dashboard route prefix (docs/research-intelligence/08).
const intentBase = "/v1/admin/intent"

const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeInternal     = "internal"
)

// Authenticator resolves a request into a verified Principal (satisfied by httpx.CtxAuthenticator).
// This package never imports httpx, so it declares the consumer-side interface.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// Deps bundles the intent-dashboard surface's collaborators. Service is the read model; Auth
// resolves the Principal behind the shared FeatureChain.
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

// Routes mounts the intent read endpoints under /v1/admin/intent. These are reads: the shared
// FeatureChain supplies session auth (CSRF exempt for GET). RBAC (IntentRead) + RLS scope the rows
// to the caller's Tenant. A platform-wide operator overview needs an enumerated operator SELECT
// policy on intent_scores (follow-on).
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: d.Service, auth: d.Auth, log: log}
	mux.HandleFunc("GET "+intentBase+"/accounts", h.read(rbac.IntentRead, h.list))
	mux.HandleFunc("GET "+intentBase+"/accounts/{domain}", h.read(rbac.IntentRead, h.account))
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

// list handles GET /v1/admin/intent/accounts — the tenant's accounts with computed intent.
func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	items, err := h.svc.List(r.Context(), limit)
	if err != nil {
		h.log.Error("intent dashboard list failed", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// account handles GET /v1/admin/intent/accounts/{domain} — the per-class scores for one account.
func (h *handlers) account(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	scores, err := h.svc.Account(r.Context(), domain)
	if err != nil {
		h.log.Error("intent dashboard account failed", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": domain, "scores": scores})
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
