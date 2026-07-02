package api

import (
	"sync"
	"time"
)

// RateLimiter is a per-key token bucket (docs/18 §6 abuse controls). Keyed by tenant, it
// bounds each tenant's request rate independently so one tenant cannot exhaust the
// gateway for others (fair-share + DoS control).
type RateLimiter struct {
	rate  float64 // tokens per second
	burst float64 // bucket capacity
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter builds a limiter allowing `rate` requests/sec with a `burst` capacity.
// If now is nil, time.Now is used.
func NewRateLimiter(rate, burst float64, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{rate: rate, burst: burst, now: now, buckets: map[string]*bucket{}}
}

// Allow reports whether a request for key may proceed, consuming one token if so.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}
	// Refill based on elapsed time.
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
