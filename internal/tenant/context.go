// Package tenant carries the authenticated principal through the call chain and is the
// single source of tenant identity (gate G1, docs/04 §4, docs/18 §1).
//
// The cardinal rule: tenant_id is read ONLY from the Principal bound here, which comes
// from the authenticated request (mTLS/JWT). It is NEVER taken from a request body,
// query parameter, or record field. Downstream stores derive their tenant scope from
// FromContext, so an application bug cannot address another tenant's data by passing a
// different id in a payload.
package tenant

import (
	"context"
	"errors"
)

// Principal is the authenticated identity of the caller. In the real system it is the
// verified subject of a signed JWT / mTLS peer; here it is the bound, immutable
// tenant scope for the request.
type Principal struct {
	TenantID string
	UserID   string   // the RBAC principal (a User, docs/00 §7); optional for machine tenants
	Scopes   []string // OAuth2/JWT scopes granted to this principal (authorization, docs/18 §1)
}

// HasScope reports whether the principal was granted scope s.
func (p Principal) HasScope(s string) bool {
	for _, have := range p.Scopes {
		if have == s {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// ErrNoPrincipal is returned when tenant-scoped work is attempted on a context that has
// no bound principal. This is fail-closed: no principal means no access, never "all
// tenants".
var ErrNoPrincipal = errors.New("tenant: no authenticated principal in context")

// WithPrincipal binds p to ctx. Call this once, at the trust boundary, right after the
// request's credential is verified.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the bound principal, or ErrNoPrincipal if none is present. Every
// tenant-scoped operation calls this rather than accepting a tenant id argument.
func FromContext(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	if !ok || p.TenantID == "" {
		return Principal{}, ErrNoPrincipal
	}
	return p, nil
}

// TenantID is a convenience that returns just the tenant id, or ErrNoPrincipal.
func TenantID(ctx context.Context) (string, error) {
	p, err := FromContext(ctx)
	if err != nil {
		return "", err
	}
	return p.TenantID, nil
}
