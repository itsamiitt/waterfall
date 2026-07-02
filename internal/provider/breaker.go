package provider

import (
	"sync"
	"time"
)

// breakerState is the classic three-state circuit breaker (docs/09 §2, gate G3). State
// is per (provider, key-pool, region); in the real system it is shared across workers
// via Redis. Here it is an in-memory, concurrency-safe cell.
type breakerState int

const (
	stateClosed   breakerState = iota // calls flow; failures are counted
	stateOpen                         // calls are rejected fast until cooldown elapses
	stateHalfOpen                     // one probe is allowed to test recovery
)

// Breaker trips open after FailureThreshold consecutive failures and stays open for
// Cooldown, then allows a single half-open probe. A success closes it; a failure
// re-opens it. Trip counting only advances on failures the caller marks retryable —
// an AUTH/BAD_REQUEST error is a caller problem, not provider ill-health.
type Breaker struct {
	FailureThreshold int
	Cooldown         time.Duration
	now              func() time.Time // injectable clock for deterministic tests

	mu       sync.Mutex
	state    breakerState
	failures int
	openedAt time.Time
}

// NewBreaker builds a breaker. If now is nil, time.Now is used.
func NewBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	if threshold < 1 {
		threshold = 1
	}
	return &Breaker{FailureThreshold: threshold, Cooldown: cooldown, now: now, state: stateClosed}
}

// Allow reports whether a call may proceed right now, transitioning Open->HalfOpen when
// the cooldown has elapsed.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case stateOpen:
		if b.now().Sub(b.openedAt) >= b.Cooldown {
			b.state = stateHalfOpen
			return true // allow exactly one probe
		}
		return false
	default: // closed or half-open (half-open allows a single probe already accounted for)
		return true
	}
}

// Open reports whether the breaker is currently rejecting calls.
func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == stateOpen && b.now().Sub(b.openedAt) < b.Cooldown
}

// RecordSuccess resets the breaker to closed.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.state = stateClosed
}

// RecordFailure advances the failure count and trips the breaker at the threshold. A
// failure in half-open immediately re-opens.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == stateHalfOpen {
		b.trip()
		return
	}
	b.failures++
	if b.failures >= b.FailureThreshold {
		b.trip()
	}
}

func (b *Breaker) trip() {
	b.state = stateOpen
	b.openedAt = b.now()
	b.failures = 0
}
