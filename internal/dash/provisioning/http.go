package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// HTTP surface for operator Tenant provisioning (doc 15 §T1, ADR-0021, doc 04 §2.1 style). The two
// endpoints have different auth requirements, so they mount via two functions: Routes (feature mux,
// behind FeatureChain) and PublicRoutes (public mux):
//
//   - POST /v1/admin/tenants — operator-only create-Tenant. This handler enforces the operator
//     role (rbac), the Idempotency-Key header, and §5.4 step-up (via the injected StepUpVerifier,
//     like keys). The orchestrator mounts it BEHIND the shared httpx FeatureChain, which supplies
//     the single authentication plus CSRF / MFA-gate / IP-allowlist enforcement — the same
//     protections the P0 surface gets, without re-authenticating (auth arrives via
//     httpx.CtxAuthenticator).
//
//   - POST /v1/admin/auth/accept-invite — PUBLIC (pre-session): the invite token IS the credential,
//     so this handler is fully self-contained and takes NO authentication. IMPORTANT for the
//     orchestrator: it MUST be mounted on the public path, NOT behind FeatureChain — FeatureChain's
//     authenticate step would reject the (deliberately unauthenticated) request with 401 before it
//     ever reaches the handler. It is Idempotency-Key exempt, consistent with the other pre-session
//     auth endpoints (login / mfa-verify; doc 04 §1.3).
//
// This package never imports internal/dash/httpx (the two surfaces stay decoupled); it depends only
// on the narrow Authenticator / StepUpVerifier contracts below, which httpx.SessionOrJWT /
// httpx.CtxAuthenticator satisfy.

const (
	basePath     = "/v1/admin"
	maxBodyBytes = 1 << 20 // 1 MiB (doc 04 §1.1)
)

// Error codes (doc 04 §1.6 registry subset used by this module).
const (
	codeInvalidJSON      = "invalid_json"
	codeMissingIdemKey   = "missing_idempotency_key"
	codeUnauthorized     = "unauthorized"
	codeMFARequired      = "mfa_required"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeConflict         = "conflict"
	codeValidationFailed = "validation_failed"
	codeInternal         = "internal"
)

// Authenticator binds the verified Principal for a request (satisfied by httpx.SessionOrJWT and,
// behind FeatureChain, httpx.CtxAuthenticator). Only the provision endpoint uses it; accept-invite
// is public.
type Authenticator interface {
	Authenticate(*http.Request) (tenant.Principal, error)
}

// StepUpVerifier verifies a per-request X-MFA-Code (§5.4 step-up). Optional: when nil, step-up is
// assumed to be enforced by an outer middleware.
type StepUpVerifier interface {
	VerifyStepUp(ctx context.Context, code string) error
}

// Deps are the constructed dependencies Routes needs.
type Deps struct {
	Store  *db.Store
	Audit  *audit.Log
	Auth   Authenticator  // provision endpoint only
	StepUp StepUpVerifier // optional (§5.4); provision endpoint
	Logger *slog.Logger
}

type router struct {
	svc    *Service
	auth   Authenticator
	stepUp StepUpVerifier
	log    *slog.Logger
}

func newRouter(d Deps) *router {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &router{
		svc:    NewService(d.Store, d.Audit, logger),
		auth:   d.Auth,
		stepUp: d.StepUp,
		log:    logger,
	}
}

// Routes mounts the operator-only provisioning write on a FEATURE mux (the orchestrator wires this
// on the shared FeatureChain, which supplies session auth / CSRF / MFA-gate / IP-allowlist). The
// handler chain adds authenticate -> RBAC (rbac.Can) -> require-idempotency -> require-step-up.
func Routes(mux *http.ServeMux, d Deps) {
	rt := newRouter(d)
	mux.HandleFunc("POST "+basePath+"/tenants",
		rt.authenticate(rt.requireRole(rbac.TenantsProvision, rt.requireIdem(rt.requireStepUp(rt.handleProvision)))))
}

// PublicRoutes mounts the pre-session accept-invite endpoint. It MUST be mounted on the PUBLIC mux
// (NOT behind FeatureChain): the invite token is the credential, so the handler is fully
// self-contained and takes no authentication — FeatureChain's authenticate step would 401 it first.
func PublicRoutes(mux *http.ServeMux, d Deps) {
	rt := newRouter(d)
	mux.HandleFunc("POST "+basePath+"/auth/accept-invite", rt.handleAcceptInvite)
}

// --- middleware ---

// authenticate binds the Principal (G1) from the injected Authenticator; failure is a uniform 401.
func (rt *router) authenticate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := rt.auth.Authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
			return
		}
		h(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}
}

// requireRole enforces the RBAC matrix (rbac.Can) for action. Tenant provisioning maps to the
// operator-only rbac.TenantsProvision row (operator allow, tenant_admin/tenant_user deny; SEC-3,
// ADR-0021), so a non-operator caller fails closed with 403 forbidden.
func (rt *router) requireRole(action rbac.Action, h http.HandlerFunc) http.HandlerFunc {
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
		h(w, r)
	}
}

// requireIdem enforces the Idempotency-Key header on the provision write (doc 04 §1.3).
func (rt *router) requireIdem(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("Idempotency-Key"); k == "" || len(k) > 128 {
			writeError(w, http.StatusBadRequest, codeMissingIdemKey, "the Idempotency-Key header is required on writes")
			return
		}
		h(w, r)
	}
}

// requireStepUp enforces §5.4 re-verification when a StepUpVerifier is wired (a create-Tenant is a
// high-authority operator action). When nil, an outer middleware is assumed to enforce it.
func (rt *router) requireStepUp(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rt.stepUp != nil {
			code := r.Header.Get("X-MFA-Code")
			if code == "" || rt.stepUp.VerifyStepUp(r.Context(), code) != nil {
				writeError(w, http.StatusUnauthorized, codeMFARequired, "step-up re-verification required")
				return
			}
		}
		h(w, r)
	}
}

// --- handlers ---

type provisionReq struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PlanTier   string `json:"plan_tier"`
	AdminEmail string `json:"admin_email"`
}

func (rt *router) handleProvision(w http.ResponseWriter, r *http.Request) {
	var req provisionReq
	if !decodeJSON(w, r, &req) {
		return
	}
	tenantID, token, err := rt.svc.ProvisionTenant(r.Context(), ProvisionRequest{
		ID:         req.ID,
		Name:       req.Name,
		PlanTier:   req.PlanTier,
		AdminEmail: req.AdminEmail,
	})
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	// Returned exactly once (the orchestrator emails it and/or hands it back). snake_case.
	writeJSON(w, http.StatusCreated, map[string]string{"tenant_id": tenantID, "invite_token": token})
}

type acceptInviteReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func (rt *router) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptInviteReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := rt.svc.AcceptInvite(r.Context(), req.Token, req.Password); err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- error mapping ---

func (rt *router) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidSlug), errors.Is(err, ErrValidation), errors.Is(err, ErrWeakPassword):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, err.Error())
	case errors.Is(err, ErrTenantExists), errors.Is(err, ErrInviteUsed):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, ErrInviteNotFound), errors.Is(err, ErrInviteExpired):
		// Existence/validity is never disclosed for a token credential (uniform 404).
		writeError(w, http.StatusNotFound, codeNotFound, "invite not found")
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		rt.log.Error("provisioning handler error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// --- json + error plumbing (uniform with httpx.writeError; snake_case bodies) ---

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
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

// writeError emits the uniform error envelope (identical shape to httpx.writeError / doc 04 §1.6).
func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}
