package api

import (
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/auth"
	"github.com/enrichment/waterfall/internal/tenant"
)

// JWTAuthenticator verifies a Bearer JWT into a tenant Principal (production path for G1).
// The tenant_id, subject, and scopes come from the verified token — never the request body.
type JWTAuthenticator struct {
	v *auth.Verifier
}

// NewJWTAuthenticator wraps a verifier as an Authenticator.
func NewJWTAuthenticator(v *auth.Verifier) *JWTAuthenticator {
	return &JWTAuthenticator{v: v}
}

// Authenticate reads the Bearer token, verifies it, and maps its claims to a Principal. Any
// verification failure returns ErrUnauthorized without leaking which check failed.
func (a *JWTAuthenticator) Authenticate(r *http.Request) (tenant.Principal, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return tenant.Principal{}, ErrUnauthorized
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	c, err := a.v.Verify(token)
	if err != nil {
		return tenant.Principal{}, ErrUnauthorized
	}
	return tenant.Principal{
		TenantID: c.TenantID,
		UserID:   c.Subject,
		Scopes:   c.Scopes,
	}, nil
}
