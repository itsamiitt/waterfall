// Package api is the HTTP gateway: it authenticates callers into a tenant principal
// (gate G1), enforces per-tenant rate limits, requires an Idempotency-Key on writes
// (docs/07, ADR-0012), validates requests, and submits work to the async job queue.
//
// The gateway holds NO provider secrets (docs/12/18): those live only at the egress tier.
package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/tenant"
)

// ErrUnauthorized indicates a missing or unrecognized credential.
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator verifies a request's credential and returns the bound principal. In
// production this validates a signed JWT / mTLS peer; the tenant_id it returns is the
// ONLY source of tenant identity downstream — request bodies never set it.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// StaticAuthenticator maps opaque bearer tokens to principals. It stands in for real JWT
// verification in the slice; the contract (token -> verified principal) is identical.
type StaticAuthenticator struct {
	tokens map[string]tenant.Principal
}

// NewStaticAuthenticator builds an authenticator from token -> principal pairs.
func NewStaticAuthenticator(tokens map[string]tenant.Principal) *StaticAuthenticator {
	cp := make(map[string]tenant.Principal, len(tokens))
	for k, v := range tokens {
		cp[k] = v
	}
	return &StaticAuthenticator{tokens: cp}
}

// Authenticate reads a bearer token from the Authorization header and resolves it.
func (a *StaticAuthenticator) Authenticate(r *http.Request) (tenant.Principal, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return tenant.Principal{}, ErrUnauthorized
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	p, ok := a.tokens[token]
	if !ok || p.TenantID == "" {
		return tenant.Principal{}, ErrUnauthorized
	}
	return p, nil
}
