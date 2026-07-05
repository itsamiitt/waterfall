package health

import (
	"context"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/provider"
)

// NewProbeCheck returns the production CheckFunc: it builds an HTTPAdapter from the Target and runs
// a single G3-bounded provider.Call (hard timeout + circuit breaker) with the credential injected
// at the egress seam from the supplied KeyResolver. It reuses internal/provider verbatim — the
// same call path the engine and the providers health-check action use.
//
// No-key safety (brief item 1): when the Provider declares an auth scheme but no resolver is wired
// (nil) or the pool can't be resolved, the probe returns a typed no-key result (down / AUTH)
// rather than crashing or making an unauthenticated call. When the Provider has no base_url it is
// reported down / BAD_REQUEST. The result never carries secret material.
func NewProbeCheck(resolver provider.KeyResolver, now func() time.Time) CheckFunc {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, t Target) CheckResult {
		region := firstRegion(t.Regions)
		if t.BaseURL == "" {
			return CheckResult{Status: StatusDown, ErrorClass: "BAD_REQUEST", Region: region}
		}

		selector := ""
		if t.AuthScheme != "" {
			if resolver == nil {
				return CheckResult{Status: StatusDown, ErrorClass: "AUTH", Region: region}
			}
			selector = t.ProviderID + ":" + defaultPoolName
			if _, err := resolver.Resolve(selector); err != nil {
				return CheckResult{Status: StatusDown, ErrorClass: "AUTH", Region: region}
			}
		}

		timeout := defaultProbeTimeout
		if t.TimeoutMS > 0 {
			timeout = time.Duration(t.TimeoutMS) * time.Millisecond
		}
		client := &http.Client{
			Timeout:   timeout + time.Second,
			Transport: provider.NewAuthInjector(nil, resolver),
		}
		adapter := &provider.HTTPAdapter{
			NameV:   t.ProviderID,
			BaseURL: t.BaseURL,
			Client:  client,
			Auth: provider.AuthDescriptor{
				Scheme:          provider.AuthScheme(t.AuthScheme),
				HeaderName:      t.AuthHeader,
				QueryParam:      t.AuthQueryParam,
				KeyPoolSelector: selector,
			},
			// A bare GET whose 2xx body we do not parse: a health probe cares about reachability
			// and status class, not payload shape.
			Decode: func([]byte) (provider.Result, error) { return provider.Result{}, nil },
		}

		threshold := t.BreakerThreshold
		if threshold < 1 {
			threshold = 5
		}
		cooldown := time.Duration(t.BreakerCooldownS) * time.Second
		if cooldown <= 0 {
			cooldown = 30 * time.Second
		}
		br := provider.NewBreaker(threshold, cooldown, nil)
		pol := provider.CallPolicy{Timeout: timeout, MaxAttempts: 1, Backoff: 50 * time.Millisecond, MaxBackoff: time.Second}

		start := now()
		_, err := provider.Call(ctx, adapter, provider.Request{}, pol, br, nil)
		lat := int(now().Sub(start).Milliseconds())
		status, class := statusForErr(err)
		return CheckResult{Status: status, LatencyMS: lat, ErrorClass: class, Region: region}
	}
}
