// Package provider defines the Adapter contract every data Provider is reached through
// and the bounded, circuit-broken call wrapper that enforces gate G3.
//
// Secret containment (ADR-0010, docs/13 §3, skills/api-integration): an Adapter NEVER
// receives a provider API key. It describes how to authenticate via an AuthDescriptor
// (scheme + key-pool selector); the egress tier injects the actual secret. A compromised
// worker/adapter therefore yields no keys. In this in-process slice the injection point
// is modelled by the Transport, which is the only component that would hold a secret.
package provider

import (
	"context"

	"github.com/enrichment/waterfall/internal/domain"
)

// AuthScheme names how a provider authenticates. The adapter picks one; it does not
// hold the credential.
type AuthScheme string

const (
	AuthAPIKeyHeader AuthScheme = "api-key-header"
	AuthAPIKeyQuery  AuthScheme = "api-key-query"
	AuthBearer       AuthScheme = "bearer"
	AuthBasic        AuthScheme = "basic"
	AuthOAuth2CC     AuthScheme = "oauth2-cc"
)

// AuthDescriptor tells the egress tier how to authenticate a call without exposing the
// secret to the adapter. KeyPoolSelector names which pool to lease a key from; the egress
// AuthInjector resolves it and places the credential per Scheme.
type AuthDescriptor struct {
	Scheme          AuthScheme
	HeaderName      string // for AuthAPIKeyHeader, e.g. "X-API-Key"
	QueryParam      string // for AuthAPIKeyQuery, e.g. "api_key"
	KeyPoolSelector string // e.g. "hunter:default" — resolved to a real key at egress
}

// Capability advertises that a Provider can return a Field, with the expected cost and
// a prior expected Confidence. The Adaptive Router (docs/08) uses these to order calls.
type Capability struct {
	Field              domain.Field
	Cost               domain.Credits
	ExpectedConfidence domain.Confidence
}

// Request is a single provider call: the match keys, the Fields wanted, and the G2
// idempotency key computed upstream.
type Request struct {
	Known          map[domain.Field]string
	Fields         []domain.Field
	IdempotencyKey string
}

// Observation is one provider-returned value with the provider's own confidence.
type Observation struct {
	Value      string
	Confidence domain.Confidence
}

// Result is the normalized output of a provider call: a value per Field it could fill.
// A Field absent from Values means "no data" (a NOT_FOUND for that field), not an error.
type Result struct {
	Values map[domain.Field]Observation
}

// Adapter is the contract for one Provider. Implementations are thin: normalize the
// request, emit an AuthDescriptor, call the Transport, map errors onto the taxonomy,
// and normalize the response into a Result.
type Adapter interface {
	Name() string
	Capabilities() []Capability
	// Fetch performs one call. It must respect ctx cancellation/deadline and return a
	// *domain.ProviderError on failure. It must not sleep past ctx.Done().
	Fetch(ctx context.Context, req Request) (Result, error)
}

// Can reports whether the adapter advertises a capability for field, returning it.
func Can(a Adapter, field domain.Field) (Capability, bool) {
	for _, c := range a.Capabilities() {
		if c.Field == field {
			return c, true
		}
	}
	return Capability{}, false
}
