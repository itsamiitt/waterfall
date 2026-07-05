package httpx

import (
	"errors"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/auth"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/tenant"
)

// ErrUnauthorized indicates a missing or unrecognized credential (mapped to 401 uniformly).
var ErrUnauthorized = errors.New("httpx: unauthorized")

// authMeta carries per-request auth facts the middleware chain needs beyond the Principal:
// whether this is a cookie session (CSRF applies), the session's CSRF token, and whether MFA is
// satisfied (verified, or not required for the role).
type authMeta struct {
	session   bool
	csrfToken string
	mfaOK     bool
}

// SessionOrJWT resolves a request into a Principal via the browser cookie session first, then a
// Bearer JWT (doc 05 §4). The cookie path binds role from the joined user; the JWT path pins the
// verification key by kid (alg-confusion safe) and requires exactly one role scope.
type SessionOrJWT struct {
	sessions *security.Sessions
	verifier *auth.Verifier // optional; nil disables the machine path
}

// NewSessionOrJWT wires the authenticator. verifier may be nil (sessions only).
func NewSessionOrJWT(sessions *security.Sessions, verifier *auth.Verifier) *SessionOrJWT {
	return &SessionOrJWT{sessions: sessions, verifier: verifier}
}

// Resolve returns the Principal and per-request auth facts, or ErrUnauthorized. It never
// distinguishes failure modes on the wire.
func (a *SessionOrJWT) Resolve(r *http.Request) (tenant.Principal, authMeta, error) {
	if ck, err := r.Cookie(sessionCookieName); err == nil && ck.Value != "" {
		sess, err := a.sessions.Resolve(r.Context(), ck.Value)
		if err != nil {
			return tenant.Principal{}, authMeta{}, ErrUnauthorized
		}
		p := tenant.Principal{
			TenantID: sess.TenantID,
			UserID:   sess.UserID,
			Scopes:   []string{"role:" + sess.Role},
		}
		return p, authMeta{
			session:   true,
			csrfToken: sess.CSRFToken,
			mfaOK:     !sess.MFARequired || sess.MFAVerified,
		}, nil
	}

	if a.verifier != nil {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if strings.HasPrefix(h, prefix) {
			claims, err := a.verifier.Verify(strings.TrimSpace(strings.TrimPrefix(h, prefix)))
			if err == nil && exactlyOneRole(claims.Scopes) {
				return tenant.Principal{
						TenantID: claims.TenantID,
						UserID:   claims.Subject,
						Scopes:   claims.Scopes,
					},
					// Machine principals are CSRF-exempt (no cookie) and cannot perform MFA
					// step-up (no seed); base auth is treated as MFA-satisfied.
					authMeta{session: false, mfaOK: true},
					nil
			}
		}
	}
	return tenant.Principal{}, authMeta{}, ErrUnauthorized
}

// Authenticate satisfies the internal/api.Authenticator-style contract for callers that only need
// the Principal.
func (a *SessionOrJWT) Authenticate(r *http.Request) (tenant.Principal, error) {
	p, _, err := a.Resolve(r)
	return p, err
}

// exactlyOneRole reports whether scopes carry exactly one role:<r> scope (doc 04 §1.2: zero or
// multiple role scopes => unauthorized).
func exactlyOneRole(scopes []string) bool {
	n := 0
	for _, s := range scopes {
		switch s {
		case "role:operator", "role:tenant_admin", "role:tenant_user":
			n++
		}
	}
	return n == 1
}
