// Package engine is the Execution Engine spine (docs/09): it runs a routed Plan while
// enforcing the five hard correctness gates before/around every provider call.
//
//	G1 tenant isolation — every store call is scoped to the context principal.
//	G2 idempotency      — a call's result is looked up by idempotency key first; a hit
//	                      is reused with no second charge.
//	G3 bounded          — each call goes through provider.Call (timeout + breaker + capped retry).
//	G4 cost ceiling     — cost is reserved BEFORE a paid call and can never exceed the
//	                      per-record ceiling; charge-on-success refunds failed/empty calls.
//	G5 provenance       — every value is persisted as a FieldValue carrying full provenance.
//
// The engine is the "deterministic gate" half of "model proposes, deterministic gate
// disposes": whatever order the router proposed, these gates are re-enforced here.
package engine

import (
	"context"
	"errors"
	"time"

	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/calibrate"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Engine executes plans against a set of adapters and a Store.
type Engine struct {
	adapters map[string]provider.Adapter
	breakers map[string]*provider.Breaker
	st       store.Store
	policy   provider.CallPolicy
	now      func() time.Time

	// metrics (nil-safe: unset means no instrumentation).
	mCalls  *metrics.Counter
	mDur    *metrics.Histogram
	mCost   *metrics.Counter
	mFilled *metrics.Counter

	// learned components (nil-safe: unset means identity calibration / no learning).
	calibrator *calibrate.Calibrator
	bandit     *bandit.Bandit
}

// Option configures an Engine.
type Option func(*Engine)

// WithPolicy overrides the default bounded call policy (G3).
func WithPolicy(p provider.CallPolicy) Option { return func(e *Engine) { e.policy = p } }

// policyFor returns the bounded CallPolicy to use for one adapter (G3). An adapter that
// implements provider.PolicyOverrider and returns a policy with Timeout>0 gets that budget
// (ADR-0024 Phase 1 — e.g. async submit→poll); otherwise the engine default applies. The
// override is still fully bounded + breaker-guarded, so G3 holds regardless.
func (e *Engine) policyFor(a provider.Adapter) provider.CallPolicy {
	if po, ok := a.(provider.PolicyOverrider); ok {
		if p := po.CallPolicy(); p.Timeout > 0 {
			return p
		}
	}
	return e.policy
}

// WithClock injects a clock for deterministic provenance timestamps / breaker timing.
func WithClock(now func() time.Time) Option { return func(e *Engine) { e.now = now } }

// WithBreaker installs a specific breaker for a provider (tests inject a fake clock).
func WithBreaker(provider string, b *provider.Breaker) Option {
	return func(e *Engine) { e.breakers[provider] = b }
}

// WithMetrics instruments the engine's provider calls into reg (docs/20): call counts by
// result, call duration, credits spent, and fields filled — all labeled by provider (no
// PII, no tenant/record ids).
func WithMetrics(reg *metrics.Registry) Option {
	return func(e *Engine) {
		e.mCalls = reg.Counter("provider_calls_total", "Provider calls by provider and result.", "provider", "result")
		e.mDur = reg.Histogram("provider_call_duration_seconds", "Provider call latency.",
			[]float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}, "provider")
		e.mCost = reg.Counter("provider_cost_credits_total", "Credits charged by provider.", "provider")
		e.mFilled = reg.Counter("enrichment_fields_filled_total", "Fields filled by field and provider.", "field", "provider")
	}
}

// WithCalibrator installs a per-(provider,field) confidence calibrator (ADR-0005). Provider
// scores are calibrated BEFORE fusion; an uncalibrated pair passes through unchanged.
func WithCalibrator(c *calibrate.Calibrator) Option {
	return func(e *Engine) { e.calibrator = c }
}

// WithBandit installs a Thompson-sampling learner (ADR-0008) that the engine UPDATES after
// each real provider call (success = the provider yielded the field). Ordering is still the
// router's job; the engine only feeds the learner. The deterministic gates are unaffected.
func WithBandit(b *bandit.Bandit) Option {
	return func(e *Engine) { e.bandit = b }
}

// New builds an Engine over the given adapters and store.
func New(st store.Store, adapters []provider.Adapter, opts ...Option) *Engine {
	e := &Engine{
		adapters: map[string]provider.Adapter{},
		breakers: map[string]*provider.Breaker{},
		st:       st,
		policy:   provider.DefaultPolicy(),
		now:      time.Now,
	}
	for _, a := range adapters {
		e.adapters[a.Name()] = a
	}
	for _, o := range opts {
		o(e)
	}
	// Give every adapter a default breaker if the caller didn't inject one.
	for name := range e.adapters {
		if e.breakers[name] == nil {
			e.breakers[name] = provider.NewBreaker(5, 30*time.Second, e.now)
		}
	}
	return e
}

// Outcome summarizes a single record enrichment.
type Outcome struct {
	Filled    map[domain.Field]domain.FieldValue // current best value per filled Field
	Committed domain.Credits                     // total credits charged for this record
	Stops     map[domain.Field]StopReason        // why each requested Field stopped
}

// StopReason explains why the engine stopped filling a Field.
type StopReason string

const (
	StopTargetMet StopReason = "target-met" // fused confidence reached the target
	StopExhausted StopReason = "exhausted"  // ran out of providers for the field
	StopCeiling   StopReason = "ceiling"    // could not afford another call within the ceiling
	StopNoTarget  StopReason = "no-target"  // no confidence target set; tried all providers
)

// Run enriches req.Subject according to plan, enforcing all five gates. It is safe to
// call twice with the same request: G2 makes the second run reuse stored results with
// no additional provider calls or charges.
func (e *Engine) Run(ctx context.Context, req domain.EnrichmentRequest, plan router.Plan) (Outcome, error) {
	// G1: establish the tenant scope up front so a missing principal fails closed before
	// any provider work.
	tenantID, err := tenant.TenantID(ctx)
	if err != nil {
		return Outcome{}, err
	}

	// T5c/OI-P4-1b: thread this job's workflow/country attribution onto the rotation lease
	// context so every leased provider call in this run emits a fully-attributed usage row.
	// Request-carried attribution (the /v1/enrichments workflow_key/country fields) takes
	// precedence; otherwise whatever the caller tagged via WithAttribution flows through. A
	// no-op when neither is set (dashboard-initiated / platform leases stay empty).
	if req.WorkflowKey != "" || req.Country != "" {
		ctx = WithAttribution(ctx, req.WorkflowKey, req.Country)
	}
	ctx = withLeaseAttribution(ctx)

	stops := map[domain.Field]StopReason{}
	for _, field := range plan.Order {
		reason, err := e.fillField(ctx, tenantID, req, field, plan.ByField[field])
		if err != nil {
			return Outcome{}, err
		}
		stops[field] = reason
	}

	filled, err := e.st.Current(ctx, req.Subject.ID)
	if err != nil {
		return Outcome{}, err
	}
	committed, err := e.st.Committed(ctx, req.JobID)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Filled: filled, Committed: committed, Stops: stops}, nil
}

// fillField runs the ordered steps for a single Field until the confidence target is
// met, providers are exhausted, or the ceiling blocks further calls.
func (e *Engine) fillField(ctx context.Context, tenantID string, req domain.EnrichmentRequest, field domain.Field, steps []router.Step) (StopReason, error) {
	values := map[string]domain.Confidence{} // per-value fused confidence
	hitCeiling := false
	target := req.ConfidenceTarget

	for _, step := range steps {
		if met(bestConfidence(values), target) {
			return StopTargetMet, nil
		}

		adapter := e.adapters[step.Provider]
		if adapter == nil {
			continue // plan named an adapter we don't have; skip defensively
		}
		key := req.IdempotencyKey(tenantID, field, step.Provider)

		// G2: reuse a prior terminal result with no second charge.
		if res, ok, err := e.st.Lookup(ctx, key); err != nil {
			return "", err
		} else if ok {
			if err := e.integrate(ctx, req.Subject.ID, field, step.Provider, res, key, 0, values); err != nil {
				return "", err
			}
			continue
		}

		// G4: reserve BEFORE the paid call. If it can't fit under the ceiling, try a
		// (possibly cheaper) later provider rather than overspending.
		if _, err := e.st.Reserve(ctx, req.JobID, step.Cost, req.CostCeiling); err != nil {
			if err == store.ErrCeilingExceeded {
				hitCeiling = true
				continue
			}
			return "", err
		}

		// G3: bounded, breaker-guarded, capped-retry call.
		callStart := e.now()
		res, callErr := provider.Call(ctx, adapter, provider.Request{
			Known:          req.Subject.Known,
			Fields:         []domain.Field{field},
			IdempotencyKey: key,
		}, e.policyFor(adapter), e.breakers[step.Provider], nil)
		e.recordCall(step.Provider, field, res, callErr, e.now().Sub(callStart))
		if e.bandit != nil {
			_, hasVal := res.Values[field]
			e.bandit.Update(step.Provider, field, callErr == nil && hasVal)
		}
		if callErr != nil {
			// charge-on-success: a failed call is not billed.
			if err := e.st.Release(ctx, req.JobID, step.Cost); err != nil {
				return "", err
			}
			continue // failover to the next provider
		}

		// Record the terminal result for idempotent replay (G2).
		if err := e.st.Record(ctx, key, res); err != nil {
			return "", err
		}
		// A call that returned no value for this field is charge-on-success: refund.
		if _, has := res.Values[field]; !has {
			if err := e.st.Release(ctx, req.JobID, step.Cost); err != nil {
				return "", err
			}
			continue
		}
		if err := e.integrate(ctx, req.Subject.ID, field, step.Provider, res, key, step.Cost, values); err != nil {
			return "", err
		}
	}

	switch {
	case met(bestConfidence(values), target):
		return StopTargetMet, nil
	case target <= 0:
		return StopNoTarget, nil
	case hitCeiling:
		return StopCeiling, nil
	default:
		return StopExhausted, nil
	}
}

// integrate fuses a provider observation into the per-value confidence map and persists
// the resulting FieldValue with full provenance (G5). cost is 0 for idempotency-cache
// hits (charge-on-success already billed the original call).
func (e *Engine) integrate(ctx context.Context, subjectID string, field domain.Field, providerName string, res provider.Result, key string, cost domain.Credits, values map[string]domain.Confidence) error {
	obs, has := res.Values[field]
	if !has {
		return nil
	}
	// Calibrate the raw provider score before fusion (ADR-0005); identity if uncalibrated.
	calibrated := obs.Confidence
	if e.calibrator != nil {
		calibrated = e.calibrator.Calibrate(providerName, field, obs.Confidence)
	}
	fused := fuseAgreeing(values[obs.Value], calibrated)
	values[obs.Value] = fused

	fv := domain.FieldValue{
		Field:      field,
		Value:      obs.Value,
		Confidence: fused,
		Prov: domain.Provenance{
			Provider:       providerName,
			ObservedAt:     e.now(),
			CostCredits:    cost,
			Confidence:     obs.Confidence,
			IdempotencyKey: key,
		},
	}
	// Metrics (nil-safe): a field was filled, and credits were charged (cost 0 on a cache hit).
	if e.mFilled != nil {
		e.mFilled.Inc(string(field), providerName)
		if cost > 0 {
			e.mCost.Add(float64(cost), providerName)
		}
	}
	// G5: the store rejects any value that fails validity, so provenance cannot be dropped.
	return e.st.Append(ctx, subjectID, fv)
}

// recordCall emits provider-call metrics (nil-safe). The result label is a bounded class,
// never PII.
func (e *Engine) recordCall(providerName string, field domain.Field, res provider.Result, callErr error, dur time.Duration) {
	if e.mCalls == nil {
		return
	}
	var result string
	switch {
	case callErr == nil:
		if _, has := res.Values[field]; has {
			result = "success"
		} else {
			result = "no_value"
		}
	case errors.Is(callErr, provider.ErrSSRFBlocked):
		result = "blocked"
	case errors.Is(callErr, provider.ErrBreakerOpen):
		result = "breaker_open"
	default:
		result = domain.ClassOf(callErr).String()
	}
	e.mCalls.Inc(providerName, result)
	e.mDur.Observe(dur.Seconds(), providerName)
}

func bestConfidence(values map[string]domain.Confidence) domain.Confidence {
	var best domain.Confidence
	for _, c := range values {
		if c > best {
			best = c
		}
	}
	return best
}
