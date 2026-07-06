package httpx

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/metrics"
)

const (
	// sessionCookieName is the browser session cookie (doc 05 §4.1). Its value is
	// "<tenant_id>|<session_id>" (Deviation D-P0-1; see internal/dash/security.Sessions).
	sessionCookieName = "dash_session"
	basePath          = "/v1/admin"
	maxBodyBytes      = 1 << 20 // 1 MiB (doc 04 §1.1)
)

// Deps bundles the constructed services a Server needs.
type Deps struct {
	Store          *db.Store
	Auth           *SessionOrJWT
	Users          *security.Users
	Sessions       *security.Sessions
	IPAllow        *security.IPAllow
	Access         *security.AccessLog
	Secrets        secrets.Backend
	Audit          *audit.Log
	Metrics        *metrics.Registry           // optional
	TrustedProxies []*net.IPNet                // optional; enables X-Forwarded-For (doc 05 §6)
	Ready          func(context.Context) error // optional; /readyz probe
	Issuer         string                      // OTP issuer label
	Logger         *slog.Logger
	Now            func() time.Time
}

// Server mounts the P0 admin surface and enforces the middleware chain
// (instrument -> authenticate -> ip-allowlist -> mfa -> csrf -> rbac -> idempotency -> audited).
type Server struct {
	store    *db.Store
	auth     *SessionOrJWT
	users    *security.Users
	sessions *security.Sessions
	ipallow  *security.IPAllow
	access   *security.AccessLog
	secrets  secrets.Backend
	audit    *audit.Log
	idem     *idemLedger
	metrics  *metrics.Registry
	proxies  []*net.IPNet
	ready    func(context.Context) error
	issuer   string
	logger   *slog.Logger
	now      func() time.Time

	reqCount *metrics.Counter
	reqDur   *metrics.Histogram
}

// NewServer wires a Server from its dependencies.
func NewServer(d Deps) *Server {
	s := &Server{
		store:    d.Store,
		auth:     d.Auth,
		users:    d.Users,
		sessions: d.Sessions,
		ipallow:  d.IPAllow,
		access:   d.Access,
		secrets:  d.Secrets,
		audit:    d.Audit,
		idem:     durableOrMemLedger(d.Store),
		metrics:  d.Metrics,
		proxies:  d.TrustedProxies,
		ready:    d.Ready,
		issuer:   d.Issuer,
		logger:   d.Logger,
		now:      d.Now,
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.issuer == "" {
		s.issuer = "Waterfall"
	}
	if s.metrics != nil {
		s.reqCount = s.metrics.Counter("dash_http_requests_total",
			"Admin HTTP requests by route, method, status.", "route", "method", "status")
		s.reqDur = s.metrics.Histogram("dash_http_request_duration_seconds",
			"Admin HTTP request latency by route.",
			[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}, "route")
	}
	return s
}

func (s *Server) log() *slog.Logger { return s.logger }

// Handler builds the routed http.Handler with the middleware chain applied per route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Ops endpoints (unauthenticated).
	mux.Handle("GET /healthz", s.instrument("/healthz", s.handleHealth))
	mux.Handle("GET /readyz", s.instrument("/readyz", s.handleReady))
	if s.metrics != nil {
		mux.Handle("GET /metrics", s.metrics.Handler())
	}

	// --- Auth & sessions (doc 04 §2.1) ---
	// login + mfa/verify are fully public (pre-session, Idempotency-Key exempt).
	s.mount(mux, "POST", "/auth/login", s.public(s.handleLogin))
	s.mount(mux, "POST", "/auth/mfa/verify", s.public(s.handleMFAVerify))
	// logout: session required, MFA + CSRF + Idempotency exempt (doc 04 §1.3).
	s.mount(mux, "POST", "/auth/logout", s.authedNoMFA(s.handleLogout))
	// enroll/confirm: session required, MFA-exempt (you are enrolling), CSRF + Idempotency required.
	s.mount(mux, "POST", "/auth/mfa/enroll", s.authedNoMFAWrite("mfa_enroll", "users", s.handleMFAEnroll))
	s.mount(mux, "POST", "/auth/mfa/enroll/confirm", s.authedNoMFAWrite("mfa_confirm", "users", s.handleMFAConfirm))
	// everything below requires a fully-authenticated (MFA-complete) principal.
	s.mount(mux, "GET", "/auth/me", s.authedRead(rbac.OverviewRead, s.handleMe))
	s.mount(mux, "GET", "/auth/sessions", s.authedRead(rbac.SessionsRevoke, s.handleSessionsList))
	s.mount(mux, "DELETE", "/auth/sessions/{id}", s.authedWrite(rbac.SessionsRevoke, "session_revoke", "sessions", s.handleSessionDelete))

	// --- Users, roles, IP allowlists (doc 04 §2.2) ---
	s.mount(mux, "GET", "/users", s.authedRead(rbac.UsersCRUD, s.handleUsersList))
	s.mount(mux, "POST", "/users", s.authedWrite(rbac.UsersCRUD, "user_create", "users", s.handleUserCreate))
	s.mount(mux, "GET", "/users/{id}", s.authedRead(rbac.UsersCRUD, s.handleUserGet))
	s.mount(mux, "PATCH", "/users/{id}", s.authedWrite(rbac.UsersCRUD, "user_update", "users", s.handleUserPatch))
	s.mount(mux, "DELETE", "/users/{id}", s.authedWrite(rbac.UsersCRUD, "user_deactivate", "users", s.handleUserDelete))
	s.mount(mux, "POST", "/users/{id}/reset-password", s.authedWrite(rbac.UsersCRUD, "user_reset_password", "users", s.handleResetPassword))
	s.mount(mux, "GET", "/roles", s.authedRead(rbac.OverviewRead, s.handleRoles))
	s.mount(mux, "GET", "/ip-allowlists", s.authedRead(rbac.UsersCRUD, s.handleIPList))
	s.mount(mux, "PUT", "/ip-allowlists", s.authedWrite(rbac.UsersCRUD, "ip_allowlist_replace", "ip_allowlists", s.handleIPPut))

	// --- Security: audit + access log (doc 04 §2.12) ---
	s.mount(mux, "GET", "/audit-log", s.authedRead(rbac.AuditRead, s.handleAuditList))
	s.mount(mux, "GET", "/audit-log/verify", s.authedRead(rbac.AuditVerify, s.handleAuditVerify))
	s.mount(mux, "GET", "/access-log", s.authedRead(rbac.AuditRead, s.handleAccessList))

	// --- Settings: per-Tenant MFA-requirement knob (SEC-5/T2, doc 04 §2.2) ---
	// The handlers are self-contained (own RBAC via rbac.MFAPolicyWrite, PATCH step-up via
	// Users.VerifyStepUp, and single-row audit with MarkAuditDone), so they mount under just the
	// cross-cutting chain — authenticate -> ip-allowlist -> [csrf on PATCH] -> require-MFA — and NOT
	// authedWrite (which would double the RBAC/audit). "settings" stays off the feature-subtree list,
	// so these fall through the admin "/" catch-all to this P0 handler.
	s.mount(mux, "GET", "/settings/mfa-policy", s.authenticate(s.ipAllow(s.requireMFA(s.HandleMFAPolicyGet))))
	s.mount(mux, "PATCH", "/settings/mfa-policy", s.authenticate(s.ipAllow(s.csrf(s.requireMFA(s.HandleMFAPolicy)))))

	return s.recoverer(mux)
}

// mount registers method+basePath+path on mux, wrapping handler in instrument with the path
// template as the route label (bounded cardinality; no ids/PII in labels).
func (s *Server) mount(mux *http.ServeMux, method, path string, h http.HandlerFunc) {
	mux.Handle(method+" "+basePath+path, s.instrument(path, h))
}

// --- route composition helpers (inner-to-outer order shown in each body) ---

// public: no authentication (login, mfa/verify).
func (s *Server) public(h http.HandlerFunc) http.HandlerFunc { return h }

// authedRead: authenticate -> ip-allowlist -> require-MFA -> require-role.
func (s *Server) authedRead(action rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return s.authenticate(s.ipAllow(s.requireMFA(s.requireRole(action, h))))
}

// authedWrite: read chain + CSRF -> idempotency -> audited.
func (s *Server) authedWrite(action rbac.Action, auditAction, kind string, h http.HandlerFunc) http.HandlerFunc {
	return s.authenticate(s.ipAllow(s.requireMFA(s.csrf(
		s.requireRole(action, s.idempotency(s.audited(auditAction, kind, h)))))))
}

// authedNoMFA: authenticated but MFA-exempt (logout), no CSRF/idempotency.
func (s *Server) authedNoMFA(h http.HandlerFunc) http.HandlerFunc {
	return s.authenticate(s.ipAllow(h))
}

// authedNoMFAWrite: authenticated, MFA-exempt, but CSRF + idempotency + audited (enroll/confirm).
func (s *Server) authedNoMFAWrite(auditAction, kind string, h http.HandlerFunc) http.HandlerFunc {
	return s.authenticate(s.ipAllow(s.csrf(s.idempotency(s.audited(auditAction, kind, h)))))
}

// --- ops handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.ready != nil {
		if err := s.ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// recoverer converts a panic in any handler into a uniform 500.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log().Error("panic in dash handler", "err", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
