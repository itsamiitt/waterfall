package provider_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
)

var noBreaker *provider.Breaker

// TestG3_TimeoutBoundsACall proves a slow provider is cut off at the policy timeout and
// classified transient, rather than hanging.
func TestG3_TimeoutBoundsACall(t *testing.T) {
	f := providertest.New("slow", "v", 0.9, 1, domain.FieldWorkEmail)
	f.Delay = 500 * time.Millisecond
	pol := provider.CallPolicy{Timeout: 20 * time.Millisecond, MaxAttempts: 1}

	start := time.Now()
	_, err := provider.Call(context.Background(), f, provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}}, pol, noBreaker, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if domain.ClassOf(err) != domain.ClassTransient {
		t.Fatalf("timeout should classify transient, got %s", domain.ClassOf(err))
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("call was not bounded by timeout: took %v", elapsed)
	}
}

// TestG3_RetriesAreBounded proves retryable errors are retried up to MaxAttempts and no
// further, and non-retryable errors are not retried at all.
func TestG3_RetriesAreBounded(t *testing.T) {
	transient := providertest.New("flaky", "v", 0.9, 1, domain.FieldWorkEmail)
	transient.Err = domain.NewProviderError("flaky", domain.ClassTransient, errors.New("boom"))
	pol := provider.CallPolicy{Timeout: time.Second, MaxAttempts: 3} // no backoff => fast

	if _, err := provider.Call(context.Background(), transient, provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}}, pol, noBreaker, nil); err == nil {
		t.Fatal("expected error")
	}
	if transient.Calls() != 3 {
		t.Fatalf("transient error should be retried to MaxAttempts=3, got %d calls", transient.Calls())
	}

	fatal := providertest.New("fatal", "v", 0.9, 1, domain.FieldWorkEmail)
	fatal.Err = domain.NewProviderError("fatal", domain.ClassBadRequest, errors.New("nope"))
	if _, err := provider.Call(context.Background(), fatal, provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}}, pol, noBreaker, nil); err == nil {
		t.Fatal("expected error")
	}
	if fatal.Calls() != 1 {
		t.Fatalf("non-retryable error must not be retried, got %d calls", fatal.Calls())
	}
}

// clock is a manually-advanced clock for deterministic breaker timing.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestG3_BreakerOpensAndRecovers proves the circuit breaker trips after the failure
// threshold (rejecting calls without invoking the provider) and half-opens after the
// cooldown.
func TestG3_BreakerOpensAndRecovers(t *testing.T) {
	clk := &clock{t: time.Unix(1700000000, 0)}
	br := provider.NewBreaker(2, 100*time.Millisecond, clk.now)

	f := providertest.New("down", "v", 0.9, 1, domain.FieldWorkEmail)
	f.Err = domain.NewProviderError("down", domain.ClassProviderDown, errors.New("outage"))
	pol := provider.CallPolicy{Timeout: time.Second, MaxAttempts: 1}
	req := provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}}

	// Two failures trip the breaker.
	_, _ = provider.Call(context.Background(), f, req, pol, br, nil)
	_, _ = provider.Call(context.Background(), f, req, pol, br, nil)
	if f.Calls() != 2 {
		t.Fatalf("want 2 provider calls before trip, got %d", f.Calls())
	}

	// Now open: the next Call must be rejected WITHOUT hitting the provider.
	_, err := provider.Call(context.Background(), f, req, pol, br, nil)
	if err == nil || domain.ClassOf(err) != domain.ClassProviderDown {
		t.Fatalf("open breaker should reject as PROVIDER_DOWN, got %v", err)
	}
	if f.Calls() != 2 {
		t.Fatalf("open breaker must not call the provider, calls=%d", f.Calls())
	}

	// After cooldown it half-opens and allows one probe (which fails here).
	clk.advance(150 * time.Millisecond)
	_, _ = provider.Call(context.Background(), f, req, pol, br, nil)
	if f.Calls() != 3 {
		t.Fatalf("half-open should allow one probe, calls=%d", f.Calls())
	}
}
