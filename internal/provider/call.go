package provider

import (
	"context"
	"errors"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// ErrBreakerOpen is returned by Call when the provider's circuit breaker is open, so no
// request is sent. It classifies as PROVIDER_DOWN.
var ErrBreakerOpen = domain.NewProviderError("", domain.ClassProviderDown, errors.New("circuit breaker open"))

// CallPolicy bounds a single provider call: a hard per-attempt Timeout, a MaxAttempts
// cap, and a Backoff base for retryable classes. These realise gate G3 (bounded, timed,
// circuit-broken) — no provider call may run unbounded.
type CallPolicy struct {
	Timeout     time.Duration
	MaxAttempts int
	Backoff     time.Duration // base backoff; attempt n waits ~Backoff*2^(n-1), capped
	MaxBackoff  time.Duration
}

// DefaultPolicy is a conservative bounded default.
func DefaultPolicy() CallPolicy {
	return CallPolicy{Timeout: 3 * time.Second, MaxAttempts: 3, Backoff: 50 * time.Millisecond, MaxBackoff: time.Second}
}

// PolicyOverrider lets an adapter request a bounded call budget different from the engine
// default (ADR-0024 Phase 1). G3 stays in force — the override is still a hard timeout +
// breaker + capped retry; only the bound changes, per adapter. Async / match→fetch adapters
// (submit→poll, token-exchange) declare a longer Timeout and usually MaxAttempts=1 (they poll
// internally within the budget rather than resubmitting). The engine consults this via a type
// assertion and falls back to its own policy when the adapter does not implement it OR returns
// a zero policy (Timeout==0), so existing adapters are unaffected.
type PolicyOverrider interface {
	CallPolicy() CallPolicy
}

// sleepFn is injectable so tests advance backoff without real waiting.
type sleepFn func(context.Context, time.Duration) error

func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Call executes one provider Fetch under the G3 guarantees: it refuses to send when the
// breaker is open, wraps each attempt in a hard timeout, retries only retryable error
// classes up to MaxAttempts with capped backoff, and feeds success/failure back to the
// breaker. It returns the first success or the last classified error.
//
// attempts, if non-nil, is incremented once per actual Fetch invocation — tests use it
// to prove idempotent replays and retry bounds.
func Call(ctx context.Context, a Adapter, req Request, pol CallPolicy, br *Breaker, attempts *int) (Result, error) {
	return callWith(ctx, a, req, pol, br, attempts, realSleep)
}

func callWith(ctx context.Context, a Adapter, req Request, pol CallPolicy, br *Breaker, attempts *int, sleep sleepFn) (Result, error) {
	if pol.MaxAttempts < 1 {
		pol.MaxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= pol.MaxAttempts; attempt++ {
		if br != nil && !br.Allow() {
			return Result{}, provErr(a.Name(), domain.ClassProviderDown, ErrBreakerOpen)
		}

		attemptCtx, cancel := context.WithTimeout(ctx, pol.Timeout)
		if attempts != nil {
			*attempts++
		}
		res, err := a.Fetch(attemptCtx, req)
		cancel()

		if err == nil {
			if br != nil {
				br.RecordSuccess()
			}
			return res, nil
		}

		// A deadline we imposed is a transient timeout against this provider.
		class := domain.ClassOf(err)
		if errors.Is(err, context.DeadlineExceeded) {
			class = domain.ClassTransient
		}
		lastErr = provErr(a.Name(), class, err)

		if br != nil && (class == domain.ClassTransient || class == domain.ClassProviderDown || class == domain.ClassRateLimit) {
			br.RecordFailure()
		}

		// If the parent context is done, stop immediately.
		if ctx.Err() != nil {
			return Result{}, lastErr
		}
		// Only retry retryable classes, and only if attempts remain.
		if !class.Retryable() || attempt == pol.MaxAttempts {
			return Result{}, lastErr
		}
		if err := sleep(ctx, backoff(pol, attempt)); err != nil {
			return Result{}, lastErr
		}
	}
	return Result{}, lastErr
}

// provErr ensures the returned error names the provider even if the adapter forgot to.
func provErr(name string, class domain.ErrorClass, err error) *domain.ProviderError {
	var pe *domain.ProviderError
	if errors.As(err, &pe) {
		if pe.Provider == "" {
			pe.Provider = name
		}
		return pe
	}
	return domain.NewProviderError(name, class, err)
}

func backoff(pol CallPolicy, attempt int) time.Duration {
	if pol.Backoff <= 0 {
		return 0
	}
	d := pol.Backoff << (attempt - 1) // Backoff * 2^(attempt-1)
	if pol.MaxBackoff > 0 && d > pol.MaxBackoff {
		d = pol.MaxBackoff
	}
	return d
}
