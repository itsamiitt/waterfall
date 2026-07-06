package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
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

// --- usage attribution ctx (T5c / OI-P4-1b) ---
//
// The enrichment engine tags the outbound call context with the job's workflow_key and subject
// country; when the egress AuthInjector draws a rotation lease for that call, the lease captures
// these and emits them on the usage row. The ctx contract lives HERE, in internal/provider (a
// dependency-free leaf both the engine and internal/dash/rotation already import), so neither the
// engine nor its binaries take a compile-time dependency on the dashboard rotation stack to set
// attribution. rotation.WithAttribution delegates to these; the engine writes them before the call.
// Both dimensions are optional and default to "" (unattributed / platform), so this is backward
// compatible with every existing call site.

type workflowCtxKey struct{}
type countryCtxKey struct{}

// WithWorkflowKey tags ctx with the workflow_key attribution carried into a leased call.
func WithWorkflowKey(ctx context.Context, workflowKey string) context.Context {
	return context.WithValue(ctx, workflowCtxKey{}, workflowKey)
}

// WithCountry tags ctx with the subject country attribution carried into a leased call.
func WithCountry(ctx context.Context, country string) context.Context {
	return context.WithValue(ctx, countryCtxKey{}, country)
}

// WithAttribution tags ctx with BOTH the workflow_key and subject country in one call.
func WithAttribution(ctx context.Context, workflowKey, country string) context.Context {
	return WithCountry(WithWorkflowKey(ctx, workflowKey), country)
}

// AttributionFromContext reads back the workflow_key/country pair; both default to "" when unset.
func AttributionFromContext(ctx context.Context) (workflowKey, country string) {
	workflowKey, _ = ctx.Value(workflowCtxKey{}).(string)
	country, _ = ctx.Value(countryCtxKey{}).(string)
	return workflowKey, country
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

	// Feature-detect the richer lease seam (rotation.LeaseResolver). When the configured resolver
	// implements it, draw a batched quota lease, inject its secret, and report the classified
	// Outcome back via Done — so the call is attributed to a key_id (G5) and feeds the KM-3 trigger
	// state machine + ai_routing posterior. StaticKeyResolver does NOT implement LeaseResolver, so
	// every existing call site keeps the plain Resolve path below unchanged (backward compatible).
	if lr, ok := a.resolver.(LeaseResolver); ok {
		return a.roundTripLeased(req, desc, lr)
	}

	secret, err := a.resolver.Resolve(desc.KeyPoolSelector)
	if err != nil {
		return nil, err
	}
	return a.base.RoundTrip(a.inject(req, desc, secret))
}

// inject clones req and places secret per the descriptor's scheme, so the secret exists only on
// the wire copy and the caller's request is never mutated.
func (a *AuthInjector) inject(req *http.Request, desc AuthDescriptor, secret string) *http.Request {
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
	return r
}

// roundTripLeased draws a lease from the LeaseResolver, injects its secret, performs the single
// round-trip, and reports the classified Outcome to lease.Done exactly once. It never logs the
// secret. Done fires whether the round-trip succeeded or failed at the transport level.
func (a *AuthInjector) roundTripLeased(req *http.Request, desc AuthDescriptor, lr LeaseResolver) (*http.Response, error) {
	lease, err := lr.Lease(req.Context(), desc.KeyPoolSelector)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, rtErr := a.base.RoundTrip(a.inject(req, desc, lease.Secret))
	if lease.Done != nil {
		lease.Done(outcomeOf(resp, rtErr, time.Since(start)))
	}
	return resp, rtErr
}

// outcomeOf classifies a completed round-trip into an Outcome using the same status->ErrorClass
// mapping the HTTPAdapter uses (classifyStatus), so the trigger state machine sees exactly the
// classes the engine would. A transport error maps by cause: SSRF refusal -> BAD_REQUEST, deadline
// / cancel -> TRANSIENT, everything else -> PROVIDER_DOWN (mirrors HTTPAdapter.Fetch).
func outcomeOf(resp *http.Response, err error, lat time.Duration) Outcome {
	latMs := int(lat.Milliseconds())
	if err != nil || resp == nil {
		class := domain.ClassProviderDown
		switch {
		case errors.Is(err, ErrSSRFBlocked):
			class = domain.ClassBadRequest
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			class = domain.ClassTransient
		}
		return Outcome{Class: class, LatencyMs: latMs, OK: false}
	}
	class, ok := classifyStatus(resp.StatusCode)
	return Outcome{Class: class, LatencyMs: latMs, OK: ok}
}
