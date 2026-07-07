package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

	// oauth2 token cache (ADR-0024 Phase 2): access tokens keyed by pool selector, exchanged
	// once and reused until expiry. Guarded by mu.
	mu     sync.Mutex
	tokens map[string]oauthToken
}

type oauthToken struct {
	token  string
	expiry time.Time
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
	injected, err := a.injectAuth(req, desc, secret)
	if err != nil {
		return nil, err
	}
	return a.base.RoundTrip(injected)
}

// injectAuth places the credential per the descriptor's scheme, returning the wire-copy request.
// For AuthOAuth2CC it first exchanges the pool secret for a cached bearer token (which may perform
// a network round-trip and can therefore fail); all other schemes are pure and never error.
func (a *AuthInjector) injectAuth(req *http.Request, desc AuthDescriptor, secret string) (*http.Request, error) {
	if desc.Scheme == AuthOAuth2CC {
		token, err := a.oauth2Token(req.Context(), desc, secret)
		if err != nil {
			return nil, err
		}
		r := req.Clone(req.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		return r, nil
	}
	return a.inject(req, desc, secret), nil
}

// inject clones req and places secret per the descriptor's scheme, so the secret exists only on
// the wire copy and the caller's request is never mutated. (oauth2-cc is handled by injectAuth.)
func (a *AuthInjector) inject(req *http.Request, desc AuthDescriptor, secret string) *http.Request {
	r := req.Clone(req.Context())
	switch desc.Scheme {
	case AuthAPIKeyHeader:
		r.Header.Set(desc.HeaderName, secret)
	case AuthAPIKeyDualHeader:
		// Two credential headers from one pool secret "first:second" (ADR-0024 Phase 4b).
		first, second, _ := strings.Cut(secret, ":")
		r.Header.Set(desc.HeaderName, first)
		r.Header.Set(desc.SecondHeaderName, second)
	case AuthAPIKeyQuery:
		q := r.URL.Query()
		q.Set(desc.QueryParam, secret)
		r.URL.RawQuery = q.Encode()
	case AuthBearer:
		r.Header.Set("Authorization", "Bearer "+secret)
	case AuthBasic:
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(secret)))
	case AuthAPIKeyPath:
		// Substitute the adapter's path sentinel with the leased key (ADR-0024 Phase 4). The
		// placeholder is a letters-only sentinel, so replacing in the decoded Path and clearing
		// RawPath lets net/url re-encode the (usually URL-safe) key correctly.
		if desc.PathPlaceholder != "" {
			r.URL.Path = strings.Replace(r.URL.Path, desc.PathPlaceholder, secret, 1)
			r.URL.RawPath = ""
		}
	}
	return r
}

// oauth2Token returns a valid client-credentials access token for the pool, exchanging the secret
// (clientId:clientSecret) at desc.TokenURL and caching the token until shortly before expiry
// (ADR-0024 Phase 2). The exchange POSTs {"grant_type":"client_credentials"} with a Basic header
// and runs through the BASE transport (SSRF-checked, non-re-entrant) — the TokenURL host must be on
// the egress allow-list. The lock is held across the exchange (coarse but correct; exchanges are
// rare — once per token lifetime per pool). The secret is never logged.
func (a *AuthInjector) oauth2Token(ctx context.Context, desc AuthDescriptor, clientSecret string) (string, error) {
	if desc.TokenURL == "" {
		return "", fmt.Errorf("oauth2-cc: no TokenURL for pool %q", desc.KeyPoolSelector)
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokens == nil {
		a.tokens = map[string]oauthToken{}
	}
	if t, ok := a.tokens[desc.KeyPoolSelector]; ok && t.token != "" && now.Before(t.expiry) {
		return t.token, nil
	}

	cid, csec, _ := strings.Cut(clientSecret, ":")
	var (
		tokReq *http.Request
		err    error
	)
	switch desc.TokenStyle {
	case "body":
		// client_id/client_secret form-encoded in the body, no Basic header (e.g. Snov.io).
		form := url.Values{"grant_type": {"client_credentials"}, "client_id": {cid}, "client_secret": {csec}}
		tokReq, err = http.NewRequestWithContext(ctx, http.MethodPost, desc.TokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	case "password":
		// OAuth2 resource-owner password grant, form-encoded (e.g. InfobelPRO); pool secret is
		// "username:password". No Basic header.
		form := url.Values{"grant_type": {"password"}, "username": {cid}, "password": {csec}}
		tokReq, err = http.NewRequestWithContext(ctx, http.MethodPost, desc.TokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	case "json":
		// JSON body with camelCase client credentials (e.g. Demandbase /auth/v1/token).
		payload, _ := json.Marshal(map[string]string{"grantType": "client_credentials", "clientId": cid, "clientSecret": csec})
		tokReq, err = http.NewRequestWithContext(ctx, http.MethodPost, desc.TokenURL, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		tokReq.Header.Set("Content-Type", "application/json")
	default:
		// Basic base64(clientId:clientSecret) + JSON grant_type body (e.g. D&B Direct+).
		tokReq, err = http.NewRequestWithContext(ctx, http.MethodPost, desc.TokenURL, strings.NewReader(`{"grant_type":"client_credentials"}`))
		if err != nil {
			return "", err
		}
		tokReq.Header.Set("Content-Type", "application/json")
		tokReq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(clientSecret)))
	}
	tokReq.Header.Set("Accept", "application/json")

	resp, err := a.base.RoundTrip(tokReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", fmt.Errorf("oauth2-cc token exchange: status %d: %s", resp.StatusCode, string(b))
	}
	var tr struct {
		AccessToken      string `json:"access_token"`
		AccessTokenCamel string `json:"accessToken"` // camelCase (e.g. Demandbase)
		ExpiresIn        int64  `json:"expiresIn"`   // D&B Direct+ camelCase
		ExpiresInStd     int64  `json:"expires_in"`  // RFC 6749 snake_case
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		tr.AccessToken = tr.AccessTokenCamel
	}
	if tr.AccessToken == "" {
		return "", errors.New("oauth2-cc token exchange: no access_token in response")
	}
	secs := tr.ExpiresIn
	if secs == 0 {
		secs = tr.ExpiresInStd
	}
	if secs <= 0 {
		secs = 3600 // conservative default when the endpoint omits expiry
	}
	// Refresh 60s before true expiry to avoid a race on a just-expiring token.
	ttl := time.Duration(secs)*time.Second - 60*time.Second
	if ttl <= 0 {
		ttl = time.Duration(secs) * time.Second
	}
	a.tokens[desc.KeyPoolSelector] = oauthToken{token: tr.AccessToken, expiry: now.Add(ttl)}
	return tr.AccessToken, nil
}

// roundTripLeased draws a lease from the LeaseResolver, injects its secret, performs the single
// round-trip, and reports the classified Outcome to lease.Done exactly once. It never logs the
// secret. Done fires whether the round-trip succeeded or failed at the transport level.
func (a *AuthInjector) roundTripLeased(req *http.Request, desc AuthDescriptor, lr LeaseResolver) (*http.Response, error) {
	lease, err := lr.Lease(req.Context(), desc.KeyPoolSelector)
	if err != nil {
		return nil, err
	}
	injected, err := a.injectAuth(req, desc, lease.Secret)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, rtErr := a.base.RoundTrip(injected)
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
