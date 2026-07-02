package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
)

// This file models the egress key-injection seam: adapters build requests WITHOUT any
// secret and attach only an AuthDescriptor; the AuthInjector RoundTripper resolves the
// key from a pool and places it as the request leaves the trust boundary (docs/12/13 §3,
// ADR-0010). A compromised adapter/worker therefore holds no credentials. The full
// SSRF-checking egress-proxy (docs/13) wraps this same seam in a later slice.

type authCtxKey struct{}

// withAuthDescriptor attaches d to ctx so the AuthInjector can find it on the outbound
// request. Called by HTTPAdapter, never by callers.
func withAuthDescriptor(ctx context.Context, d AuthDescriptor) context.Context {
	return context.WithValue(ctx, authCtxKey{}, d)
}

func authDescriptorFrom(ctx context.Context) (AuthDescriptor, bool) {
	d, ok := ctx.Value(authCtxKey{}).(AuthDescriptor)
	return d, ok
}

// KeyResolver leases the secret for a key-pool selector. In production this is backed by
// the secrets manager / Vault at the egress-proxy; adapters never see it.
type KeyResolver interface {
	Resolve(poolSelector string) (secret string, err error)
}

// StaticKeyResolver is a fixed pool->secret map for tests and local runs.
type StaticKeyResolver map[string]string

// Resolve returns the secret for a pool, or an error if the pool is unknown.
func (s StaticKeyResolver) Resolve(pool string) (string, error) {
	secret, ok := s[pool]
	if !ok {
		return "", fmt.Errorf("no key for pool %q", pool)
	}
	return secret, nil
}

// AuthInjector is an http.RoundTripper that injects the resolved credential per the
// request's AuthDescriptor. It clones the request so the caller's request is never mutated
// and the secret exists only on the wire copy.
type AuthInjector struct {
	base     http.RoundTripper
	resolver KeyResolver
}

// NewAuthInjector wraps base with credential injection driven by the request's
// AuthDescriptor. If base is nil, http.DefaultTransport is used.
func NewAuthInjector(base http.RoundTripper, resolver KeyResolver) *AuthInjector {
	if base == nil {
		base = http.DefaultTransport
	}
	return &AuthInjector{base: base, resolver: resolver}
}

func (a *AuthInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	desc, ok := authDescriptorFrom(req.Context())
	if !ok || desc.KeyPoolSelector == "" {
		return a.base.RoundTrip(req) // no auth needed
	}
	secret, err := a.resolver.Resolve(desc.KeyPoolSelector)
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	switch desc.Scheme {
	case AuthAPIKeyHeader:
		r.Header.Set(desc.HeaderName, secret)
	case AuthAPIKeyQuery:
		q := r.URL.Query()
		q.Set(desc.QueryParam, secret)
		r.URL.RawQuery = q.Encode()
	case AuthBearer:
		r.Header.Set("Authorization", "Bearer "+secret)
	case AuthBasic:
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(secret)))
	}
	return a.base.RoundTrip(r)
}
