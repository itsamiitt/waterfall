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
	AuthAPIKeyHeader     AuthScheme = "api-key-header"
	AuthAPIKeyQuery      AuthScheme = "api-key-query"
	AuthAPIKeyPath       AuthScheme = "api-key-path"        // key is a URL path segment (ADR-0024 Phase 4a)
	AuthAPIKeyDualHeader AuthScheme = "api-key-dual-header" // two credential headers (ADR-0024 Phase 4b)
	AuthBearer           AuthScheme = "bearer"
	AuthBasic            AuthScheme = "basic"
	AuthOAuth2CC         AuthScheme = "oauth2-cc"
)

// AuthDescriptor tells the egress tier how to authenticate a call without exposing the
// secret to the adapter. KeyPoolSelector names which pool to lease a key from; the egress
// AuthInjector resolves it and places the credential per Scheme.
type AuthDescriptor struct {
	Scheme          AuthScheme
	HeaderName      string // for AuthAPIKeyHeader, e.g. "X-API-Key"
	QueryParam      string // for AuthAPIKeyQuery, e.g. "api_key"
	KeyPoolSelector string // e.g. "hunter:default" — resolved to a real key at egress
	// TokenURL is the OAuth2 client-credentials token endpoint, required for AuthOAuth2CC
	// (ADR-0024 Phase 2). The egress AuthInjector exchanges the pool secret ("clientId:clientSecret")
	// for a short-lived access token at this URL, caches it until expiry, and injects it as a
	// Bearer token. The adapter still never holds the secret.
	TokenURL string
	// TokenStyle selects how the client-credentials are presented at TokenURL (AuthOAuth2CC):
	// "" / "basic" = HTTP Basic base64(clientId:clientSecret) header + {"grant_type":...} JSON body
	// (e.g. D&B Direct+); "body" = form-encoded grant_type/client_id/client_secret in the request
	// body, no Basic header (e.g. Snov.io). The pool secret is "clientId:clientSecret" either way.
	TokenStyle string
	// PathPlaceholder is the literal path substring the adapter's Build writes where the key belongs,
	// for AuthAPIKeyPath (ADR-0024 Phase 4a — e.g. MixRank's /v2/json/{key}/…). The egress AuthInjector
	// replaces the first occurrence with the leased secret; the adapter still never holds it. Use a
	// path-safe sentinel (letters only) to avoid URL-encoding surprises.
	PathPlaceholder string
	// SecondHeaderName is the second credential header for AuthAPIKeyDualHeader (ADR-0024 Phase 4b —
	// e.g. PredictLeads' X-Api-Key + X-Api-Token). The pool secret carries both values joined as
	// "first:second"; the injector splits and sets HeaderName←first, SecondHeaderName←second.
	SecondHeaderName string
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

// Introspectable exposes the static integration descriptor (base URL + auth) that the catalog
// seeder and the SSRF allow-list read from a constructed adapter, beyond the Adapter surface. Both
// HTTPAdapter and AsyncHTTPAdapter satisfy it, so the registry can hold either kind and the seeder /
// Hosts() introspect them uniformly (ADR-0023/0024).
type Introspectable interface {
	Adapter
	Base() string
	AuthDescriptor() AuthDescriptor
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
