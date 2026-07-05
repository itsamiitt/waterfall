package provider

import (
	"context"

	"github.com/enrichment/waterfall/internal/domain"
)

// This file defines the richer key-resolution seam the egress AuthInjector feature-detects on
// its KeyResolver. It lives HERE, in internal/provider (a leaf-ish package that already imports
// domain), rather than in internal/dash/rotation, precisely to avoid an import cycle:
// internal/dash/rotation depends on internal/provider (for provider.Call, HTTPAdapter, Breaker,
// AuthInjector, and these types) and implements LeaseResolver — provider never imports rotation.
// The AuthInjector uses a plain type assertion (a.resolver.(LeaseResolver)); StaticKeyResolver
// does not implement it, so every existing call site keeps the plain Resolve path unchanged
// (backward compatible — doc 12 P2, MASTER SPEC §5 "Engine integration").

// Outcome is the classified result of one leased provider call, reported back through Lease.Done
// after the HTTP response completes. The rotation engine uses it to update the key's EWMA
// latency/success, drive the KM-3 trigger state machine (AUTH -> auth_failed, QUOTA -> exhausted,
// sustained RATE_LIMIT -> rate_limited), and update the ai_routing Beta-Thompson posterior. It
// carries no secret material and no response body — only the taxonomy class, latency, and OK bit.
type Outcome struct {
	Class     domain.ErrorClass // the 8-class taxonomy class; ClassUnknown on a 2xx success
	LatencyMs int               // wall-clock latency of the single HTTP round-trip
	OK        bool              // true iff the response was a 2xx success
}

// Lease is one leased Provider Key: the resolved secret plus the key_id the call is attributed to
// (G5 provenance) and a Done closure the egress path MUST invoke exactly once after the response
// completes, reporting the Outcome. Secret is never logged by any holder; the AuthInjector places
// it on the wire copy of the request and discards it.
type Lease struct {
	KeyID  string
	Secret string
	Done   func(Outcome)
}

// LeaseResolver is the batched-lease + attribution seam. When the AuthInjector's configured
// KeyResolver also implements LeaseResolver, the injector uses Lease(...) INSTEAD of Resolve to
// obtain the secret, and calls the returned Done(outcome) after the round-trip — so every engine
// call draws a batched quota lease, is attributed to a key_id, and feeds the trigger state machine.
// internal/dash/rotation.Engine implements this interface.
type LeaseResolver interface {
	Lease(ctx context.Context, poolSelector string) (Lease, error)
}
