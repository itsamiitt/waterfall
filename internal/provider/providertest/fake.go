// Package providertest offers a configurable fake Adapter used across engine and router
// tests. It records how many times it was actually called so tests can prove idempotent
// replay (G2) and bounded retries (G3).
package providertest

import (
	"context"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Fake is a programmable Adapter. Zero value is not useful; use New.
type Fake struct {
	NameV string
	Caps  []provider.Capability

	// Behaviour knobs:
	Value    string                  // value returned for every requested field (unless NoValue)
	Conf     domain.Confidence       // provider-reported confidence
	Err      error                   // if non-nil, Fetch returns this (already classified) error
	Delay    time.Duration           // sleep before responding, honouring ctx (to trigger timeouts)
	NoValue  map[domain.Field]bool   // fields for which to return success-with-no-value
	PerField map[domain.Field]string // override value per field

	mu    sync.Mutex
	calls int
}

// New builds a Fake that can fill the given fields at the given cost/confidence.
func New(name, value string, conf domain.Confidence, cost domain.Credits, fields ...domain.Field) *Fake {
	caps := make([]provider.Capability, 0, len(fields))
	for _, f := range fields {
		caps = append(caps, provider.Capability{Field: f, Cost: cost, ExpectedConfidence: conf})
	}
	return &Fake{NameV: name, Caps: caps, Value: value, Conf: conf}
}

func (f *Fake) Name() string { return f.NameV }

func (f *Fake) Capabilities() []provider.Capability { return f.Caps }

// Calls returns the number of times Fetch actually executed.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *Fake) Fetch(ctx context.Context, req provider.Request) (provider.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.Delay > 0 {
		t := time.NewTimer(f.Delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return provider.Result{}, ctx.Err()
		case <-t.C:
		}
	}
	if f.Err != nil {
		return provider.Result{}, f.Err
	}
	out := provider.Result{Values: map[domain.Field]provider.Observation{}}
	for _, field := range req.Fields {
		if f.NoValue[field] {
			continue
		}
		val := f.Value
		if v, ok := f.PerField[field]; ok {
			val = v
		}
		out.Values[field] = provider.Observation{Value: val, Confidence: f.Conf}
	}
	return out, nil
}
