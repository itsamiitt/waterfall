package engine

import (
	"context"

	"github.com/enrichment/waterfall/internal/provider"
)

// This file is the engine's usage-attribution seam (T5c / OI-P4-1b). An enrichment job knows its
// workflow_key and subject country; the rotation lease that serves each provider call needs them so
// the emitted usage_events row is fully attributed (doc 12 §P4). The engine sits between the two: it
// carries the attribution on the request context and, in Run, threads it onto the outbound provider
// call context ahead of every provider/lease call.
//
// The engine writes the provider-level egress attribution contract (internal/provider, a leaf) — NOT
// rotation.WithAttribution directly — so the core engine and its binaries take no compile-time
// dependency on the dashboard rotation stack. rotation.WithAttribution delegates to the same
// provider contract, so a rotation lease drawn on this context reads exactly what the engine set.
// Keeping the caller-facing seam in engine terms (WithAttribution here) lets enrichd/enrichapi set
// attribution without knowing the egress ctx internals.

// attribKey is the private context key carrying an enrichment job's usage attribution.
type attribKey struct{}

// attribution is one job's usage-attribution dimensions.
type attribution struct {
	workflowKey string
	country     string
}

// WithAttribution tags ctx with the enrichment job's workflow_key and subject country. The caller
// (enrichd, from the EnrichmentRequest/job) sets it before Engine.Run; Run threads it onto every
// leased provider call via rotation.WithAttribution. Unset (dashboard-initiated / unattributed work)
// leaves leases platform/empty, so it is backward-compatible. Empty strings are treated as unset.
func WithAttribution(ctx context.Context, workflowKey, country string) context.Context {
	return context.WithValue(ctx, attribKey{}, attribution{workflowKey: workflowKey, country: country})
}

func attributionFrom(ctx context.Context) attribution {
	a, _ := ctx.Value(attribKey{}).(attribution)
	return a
}

// withLeaseAttribution translates the engine's job attribution (if any) onto the provider egress
// context so a leased provider call draws a fully-attributed usage row. It is a no-op — returning
// ctx unchanged — when neither dimension is set, preserving prior behavior and adding zero overhead
// on the unattributed path.
func withLeaseAttribution(ctx context.Context) context.Context {
	a := attributionFrom(ctx)
	if a.workflowKey == "" && a.country == "" {
		return ctx
	}
	return provider.WithAttribution(ctx, a.workflowKey, a.country)
}
