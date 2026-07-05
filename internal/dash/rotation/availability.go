// Package rotation is the Key Rotation Engine (module 4, doc 07 §8/§9, MASTER SPEC §5): per-pool
// in-memory selection (12 strategies, O(1) lock-cheap hot path), batched quota leases against
// key_budgets (no over-lease under concurrency), the KM-3 Provider Key trigger state machine, and
// the rotation.Engine that satisfies provider.LeaseResolver so every egress call draws a lease, is
// attributed to a key_id (G5), and feeds the state machine + the ai_routing bandit.
//
// Gates / invariants enforced:
//   - No over-lease (pool safety, doc 12 P2 #1). Leases are drawn from an in-memory token bucket
//     refilled ONLY by the guarded atomic UPDATE key_budgets ... WHERE day_leased + $2 <=
//     daily_limit (batch <= 64); the sum of granted batches can never exceed daily_limit, so a
//     50-goroutine storm grants <= N. A crash loses at most one batch (reclaimed by Reconcile).
//   - O(1) lock-cheap selection (horizontal scale, doc 12 P2 #2). The hot path uses atomics
//     (round-robin cursor, per-key availability, banded snapshots via atomic.Pointer) and an
//     immutable key slice; only rebuild / re-band take a write path, off the hot path.
//   - G5 provenance. Lease.Done attributes each call's Outcome to its key_id and updates that
//     key's EWMA / bandit posterior; usage attribution rides the lease regardless of key state.
//   - Class P tenancy (G1). provider_keys / key_budgets / rotation_triggers are platform tables;
//     every read/write runs through db.Store.PlatformTx.
//   - No secrets/PII in logs or errors. Key material lives only on the Lease.Secret returned to the
//     egress injector; it is never logged, audited, or placed in an error.
package rotation

import "github.com/enrichment/waterfall/internal/dash/keys"

// KeyAvailable is THE one key-availability function (doc 07 §9 "rotation.KeyAvailable"), the KM-3
// analogue of providers.EffectiveAvailability: it collapses a Provider Key's status axis into a
// single "may this key serve a lease NOW?" bit. The status vocabulary is the provider_keys.status
// CHECK (migration 0005), reused from internal/dash/keys so there is one source of truth.
//
//	active      -> available (usable now)
//	rotating    -> available (overlap window: old + new keys are BOTH valid during cutover, §9)
//	paused      -> unavailable (manual pause / timeout-pending health)
//	exhausted   -> unavailable (QUOTA; auto re-enable via recovery probe)
//	rate_limited-> unavailable (RATE_LIMIT cooldown; auto-recovers)
//	auth_failed -> unavailable (AUTH; parks to disabled, manual re-enable only)
//	disabled    -> unavailable (manual re-enable only)
//	expired     -> unavailable (expires_at reached)
//	archived    -> unavailable (terminal, never usable)
//
// The full effective conjunction (inclusion status x op_state x key state x breaker x budget) is
// assembled by the lease path: this covers the key-state conjunct, the token bucket covers budget,
// and the provider Breaker covers the breaker conjunct independently (breaker-open is orthogonal to
// status, doc 07 §9). Unknown statuses fail closed to unavailable.
func KeyAvailable(status string) bool {
	switch status {
	case keys.StatusActive, keys.StatusRotating:
		return true
	default:
		return false
	}
}
