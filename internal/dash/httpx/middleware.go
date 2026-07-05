package httpx

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/tenant"
)

// context keys for per-request state threaded through the chain.
type (
	ctxKeyMeta  struct{}
	ctxKeyRec   struct{}
	ctxKeyAudit struct{}
)

// accessRecord accumulates the fields the instrument middleware emits to api_access_log; the
// authenticate middleware fills tenant/user once the Principal is bound.
type accessRecord struct {
	tenantID string
	userID   string
	route    string
	method   string
	ip       string
	status   int
}

// auditInfo lets a handler enrich the audited() wrapper with an object id and an after-snapshot.
type auditInfo struct {
	objectID string
	after    json.RawMessage
}

// statusWriter captures the response status for metrics/logging/audit.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// instrument records RED signals + a structured log line, and emits one api_access_log record
// (route TEMPLATE only). It is outermost per route so it observes the final status.
func (s *Server) instrument(route string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		rec := &accessRecord{route: route, method: r.Method, ip: s.clientIP(r)}
		ctx := context.WithValue(r.Context(), ctxKeyRec{}, rec)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h(sw, r.WithContext(ctx))
		rec.status = sw.status
		dur := s.now().Sub(start)
		if s.reqCount != nil {
			s.reqCount.Inc(route, r.Method, strconv.Itoa(sw.status))
			s.reqDur.Observe(dur.Seconds(), route)
		}
		if s.access != nil {
			s.access.Enqueue(security.AccessRecord{
				TenantID: rec.tenantID, UserID: rec.userID, Method: r.Method,
				Route: route, Status: sw.status, DurMs: int(dur.Milliseconds()), IP: rec.ip,
			})
		}
		s.log().Info("dash_http_request", "method", r.Method, "route", route,
			"status", sw.status, "dur_ms", dur.Milliseconds())
	}
}

// authenticate binds the Principal (G1) and the per-request auth facts into the context. Failure
// is a uniform 401.
func (s *Server) authenticate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, meta, err := s.auth.Resolve(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
			return
		}
		if rec := recFrom(r.Context()); rec != nil {
			rec.tenantID = p.TenantID
			rec.userID = p.UserID
		}
		ctx := tenant.WithPrincipal(r.Context(), p)
		ctx = context.WithValue(ctx, ctxKeyMeta{}, meta)
		h(w, r.WithContext(ctx))
	}
}

// ipAllow enforces the tenant's CIDR allowlist after authentication (doc 05 §6). An evaluation
// error is treated as allowed (fail-open on a parse hiccup never locks a tenant out; absence of
// rows already means unrestricted).
func (s *Server) ipAllow(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.ipallow.Allowed(r.Context(), s.clientIP(r))
		if err == nil && !ok {
			writeError(w, http.StatusForbidden, codeIPNotAllowed, "caller ip outside tenant allowlist")
			return
		}
		h(w, r)
	}
}

// requireMFA rejects a session that has not completed required MFA (doc 05 §5.3): every route but
// the enrollment ones is gated until the step-up is done.
func (s *Server) requireMFA(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m := metaFrom(r.Context()); !m.mfaOK {
			writeError(w, http.StatusUnauthorized, codeMFARequired, "multi-factor authentication required")
			return
		}
		h(w, r)
	}
}

// csrf enforces the double-submit check for cookie-session mutating requests (doc 05 §4.1). The
// JWT path carries no cookie and is exempt. Missing header => csrf_required; mismatch => csrf_invalid.
func (s *Server) csrf(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := metaFrom(r.Context())
		if m.session {
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				writeError(w, http.StatusForbidden, codeCSRFRequired, "X-CSRF-Token header is required")
				return
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(m.csrfToken)) != 1 {
				writeError(w, http.StatusForbidden, codeCSRFInvalid, "X-CSRF-Token does not match session")
				return
			}
		}
		h(w, r)
	}
}

// requireRole is the RBAC guard: rbac.Can must return a non-deny decision (doc 05 §2).
// DecisionApprovalGated is treated as permitted here — the 202 quorum flow is a handler concern.
func (s *Server) requireRole(action rbac.Action, h http.HandlerFunc) http.HandlerFunc {
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

// --- context accessors ---

func recFrom(ctx context.Context) *accessRecord {
	rec, _ := ctx.Value(ctxKeyRec{}).(*accessRecord)
	return rec
}

func metaFrom(ctx context.Context) authMeta {
	m, _ := ctx.Value(ctxKeyMeta{}).(authMeta)
	return m
}

func auditFrom(ctx context.Context) *auditInfo {
	ai, _ := ctx.Value(ctxKeyAudit{}).(*auditInfo)
	return ai
}

// RecordAudit lets a handler set the audited() wrapper's object id and after-snapshot. Snapshots
// MUST use string/int/bool fields only (never floats) so the hash chain re-canonicalizes
// identically after a Postgres jsonb round-trip (doc 05 §8.1).
func RecordAudit(ctx context.Context, objectID string, after any) {
	ai := auditFrom(ctx)
	if ai == nil {
		return
	}
	ai.objectID = objectID
	if after != nil {
		if b, err := json.Marshal(after); err == nil {
			ai.after = b
		}
	}
}

// clientIP returns the effective client address (doc 05 §6): the direct peer by default;
// X-Forwarded-For is honored only when the peer is a configured trusted proxy.
func (s *Server) clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if len(s.proxies) == 0 {
		return host
	}
	peer := net.ParseIP(host)
	trusted := false
	for _, n := range s.proxies {
		if peer != nil && n.Contains(peer) {
			trusted = true
			break
		}
	}
	if !trusted {
		return host
	}
	// Rightmost XFF entry not belonging to a trusted proxy.
	xff := r.Header.Get("X-Forwarded-For")
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		cand := strings.TrimSpace(parts[i])
		ip := net.ParseIP(cand)
		if ip == nil {
			continue
		}
		inTrusted := false
		for _, n := range s.proxies {
			if n.Contains(ip) {
				inTrusted = true
				break
			}
		}
		if !inTrusted {
			return cand
		}
	}
	return host
}
