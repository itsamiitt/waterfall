package httpx

import (
	"context"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/tenant"
)

// setSessionCookie writes the dash_session cookie (HttpOnly/Secure/SameSite=Lax, doc 05 §4.1).
func setSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// userCtx builds a context bound to a user's Principal, used to run session/audit writes on the
// pre-session login/MFA paths where no Principal is in the request context yet.
func userCtx(parent context.Context, u security.User) context.Context {
	return tenant.WithPrincipal(parent, tenant.Principal{
		TenantID: u.TenantID,
		UserID:   u.ID,
		Scopes:   []string{"role:" + u.Role},
	})
}

// appendAudit writes an audit row under ctx's Principal, logging (never failing the request) on
// error. Snapshots are string-valued so the chain re-canonicalizes identically (doc 05 §8.1).
func (s *Server) appendAudit(ctx context.Context, e audit.Entry) {
	if err := s.audit.Append(ctx, e); err != nil {
		s.log().Error("audit append failed", "action", e.Action, "err", err)
	}
}

// handleLogin verifies email+password (PBKDF2, constant-time, timing-equalized unknown email),
// starts a session, and returns mfa_required or the ok summary (doc 05 §4.3).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	usr, pwHash, found, err := s.users.AuthLookup(r.Context(), body.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "login failed")
		return
	}
	if !found {
		security.VerifyPassword(body.Password, security.DummyHash()) // equalize timing
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid email or password")
		return
	}
	if !security.VerifyPassword(body.Password, pwHash) || usr.Status != "active" {
		s.appendAudit(userCtx(r.Context(), usr), audit.Entry{
			Action: "login_failed", ObjectKind: "session", ActorUserID: usr.ID, ActorRole: usr.Role,
			IP: s.clientIP(r), After: jsonStr(map[string]string{"result": "denied"}),
		})
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid email or password")
		return
	}

	uctx := userCtx(r.Context(), usr)
	cookie, csrf, err := s.sessions.Create(uctx, usr.ID, s.clientIP(r), r.UserAgent(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "login failed")
		return
	}
	setSessionCookie(w, cookie)

	// SEC-5 (T2): a tenant_admin may require MFA for every User of the Tenant (tenants.require_mfa).
	// Read it under the user's own Tenant binding. An unenrolled User in a require-MFA Tenant gets the
	// documented mfa_enrollment_required status: the session is started (cookie set) but the Resolve
	// MFA gate keeps it unusable for everything but the MFA-exempt enrollment endpoints until the User
	// enrolls + verifies, so the SPA routes straight to enrollment.
	tenantRequiresMFA, err := s.tenantPolicy().RequireMFA(uctx, usr.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "login failed")
		return
	}

	mfaRequired := security.RequiresMFA(usr.Role) || usr.MFAEnrolled || tenantRequiresMFA
	// Enrollment gate: whenever MFA is needed (role-mandated, tenant-mandated via require_mfa, or the
	// User already carries a seed) but the User has NOT yet enrolled a TOTP secret, return the
	// documented mfa_enrollment_required status. The session is started but the Resolve MFA gate keeps
	// it usable only for the MFA-exempt enrollment endpoints. The CSRF token is returned so the SPA can
	// POST the (CSRF-protected, MFA-exempt) /auth/mfa/enroll — the first tenant_admin created by
	// provisioning (T1) and every unenrolled User of a require_mfa Tenant (T2) onboard through exactly
	// this path; without the token the enroll write would 403 csrf_required and onboarding would wedge.
	if mfaRequired && usr.MFAEnvelopeID == "" {
		s.appendAudit(uctx, audit.Entry{
			Action: "login", ObjectKind: "session", ActorUserID: usr.ID, ActorRole: usr.Role,
			IP: s.clientIP(r), After: jsonStr(map[string]string{"status": statusMFAEnrollmentRequired}),
		})
		writeJSON(w, http.StatusOK, sessionOK{Status: statusMFAEnrollmentRequired, CSRFToken: csrf, User: toUserSummary(usr)})
		return
	}

	statusStr := "ok"
	if mfaRequired {
		statusStr = "mfa_required"
	}
	s.appendAudit(uctx, audit.Entry{
		Action: "login", ObjectKind: "session", ActorUserID: usr.ID, ActorRole: usr.Role,
		IP: s.clientIP(r), After: jsonStr(map[string]string{"status": statusStr}),
	})

	if mfaRequired {
		writeJSON(w, http.StatusOK, map[string]string{"status": "mfa_required"})
		return
	}
	writeJSON(w, http.StatusOK, sessionOK{Status: "ok", CSRFToken: csrf, User: toUserSummary(usr)})
}

// statusMFAEnrollmentRequired is the documented login status (T2/SEC-5) returned when the User's
// Tenant requires MFA but the User has not yet enrolled a TOTP seed. The session is established but
// gated; the SPA routes the User to POST /auth/mfa/enroll (which is MFA-exempt).
const statusMFAEnrollmentRequired = "mfa_enrollment_required"

// handleMFAVerify completes login with a TOTP or recovery code, rotates the session id (fixation
// defense), stamps mfa_verified_at, and (re)issues the CSRF token (doc 05 §4.3/§5).
func (s *Server) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(sessionCookieName)
	if err != nil || ck.Value == "" {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "no session")
		return
	}
	sess, err := s.sessions.Resolve(r.Context(), ck.Value)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "no session")
		return
	}
	var body codeRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	uctx := tenant.WithPrincipal(r.Context(), tenant.Principal{
		TenantID: sess.TenantID, UserID: sess.UserID, Scopes: []string{"role:" + sess.Role},
	})

	// VerifyAndConsume verifies the TOTP AND records the (user, time_step) single-use marker so a
	// captured code cannot be replayed inside its ±1-step window (OI-SEC-8; mfa_used_steps).
	ok := false
	if v, verr := s.users.VerifyAndConsume(uctx, sess.UserID, body.Code, s.now()); verr == nil && v {
		ok = true
	}
	if !ok {
		if consumed, cerr := s.users.ConsumeRecoveryCode(uctx, sess.UserID, body.Code); cerr == nil && consumed {
			ok = true
		}
	}
	if !ok {
		s.appendAudit(uctx, audit.Entry{
			Action: "login_failed", ObjectKind: "session", ObjectID: sess.ID,
			ActorUserID: sess.UserID, ActorRole: sess.Role, IP: s.clientIP(r),
			After: jsonStr(map[string]string{"result": "mfa_denied"}),
		})
		writeError(w, http.StatusUnauthorized, codeMFARequired, "invalid or expired code")
		return
	}

	_, _ = s.sessions.Revoke(uctx, sess.ID)
	cookie, csrf, err := s.sessions.Create(uctx, sess.UserID, s.clientIP(r), r.UserAgent(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "verify failed")
		return
	}
	setSessionCookie(w, cookie)
	s.appendAudit(uctx, audit.Entry{
		Action: "login", ObjectKind: "session", ActorUserID: sess.UserID, ActorRole: sess.Role,
		IP: s.clientIP(r), After: jsonStr(map[string]string{"mfa": "verified"}),
	})

	usr, _ := s.users.GetByID(uctx, sess.UserID)
	if usr.ID == "" {
		usr = security.User{ID: sess.UserID, TenantID: sess.TenantID, Role: sess.Role, MFAEnrolled: true}
	}
	writeJSON(w, http.StatusOK, sessionOK{Status: "ok", CSRFToken: csrf, User: toUserSummary(usr)})
}

// handleMFAEnroll begins TOTP enrollment: seals a fresh seed and returns the provisioning URI once.
func (s *Server) handleMFAEnroll(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	usr, err := s.users.GetByID(r.Context(), p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "enroll failed")
		return
	}
	_, url, err := s.users.EnrollMFA(r.Context(), p.UserID, usr.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "enroll failed")
		return
	}
	RecordAudit(r.Context(), p.UserID, map[string]string{"action": "mfa_enroll_begin"})
	writeJSON(w, http.StatusOK, map[string]string{"otpauth_url": url})
}

// handleMFAConfirm confirms enrollment with the first code and returns recovery codes once.
func (s *Server) handleMFAConfirm(w http.ResponseWriter, r *http.Request) {
	var body codeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	codes, err := s.users.ConfirmMFA(r.Context(), p.UserID, body.Code, s.now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeMFARequired, "invalid or expired code")
		return
	}
	RecordAudit(r.Context(), p.UserID, map[string]string{"action": "mfa_confirmed"})
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// handleLogout revokes the current session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	if ck, err := r.Cookie(sessionCookieName); err == nil {
		if _, id, ok := splitCookieValue(ck.Value); ok {
			_, _ = s.sessions.Revoke(r.Context(), id)
		}
	}
	s.appendAudit(r.Context(), audit.Entry{
		Action: "logout", ObjectKind: "session", ActorUserID: p.UserID,
		IP: s.clientIP(r), After: jsonStr(map[string]string{"result": "revoked"}),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMe returns the current user, role, tenant, and session expiry (SPA bootstrap, doc 04 §2.1).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	usr, err := s.users.GetByID(r.Context(), p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":      toUserSummary(usr),
		"role":      usr.Role,
		"tenant_id": p.TenantID,
	})
}

// handleSessionsList lists sessions (own; tenant_admin+ sees the whole tenant).
func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	all := roleOf(p) != "tenant_user"
	items, err := s.sessions.List(r.Context(), p.UserID, all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "list failed")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, si := range items {
		out = append(out, map[string]any{
			"id":                  si.ID,
			"user_id":             si.UserID,
			"created_at":          si.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			"idle_expires_at":     si.IdleExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
			"absolute_expires_at": si.AbsoluteExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
			"mfa_verified":        si.MFAVerified,
		})
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: out, NextCursor: nil})
}

// handleSessionDelete revokes a session by id (404 across tenants, doc 04 §2.1).
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	changed, err := s.sessions.Revoke(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "revoke failed")
		return
	}
	if !changed {
		writeError(w, http.StatusNotFound, codeNotFound, "session not found")
		return
	}
	RecordAudit(r.Context(), id, map[string]string{"result": "revoked"})
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
