package rotation

// StrategyInfo is one entry of the rotation strategy catalog served by
// GET /v1/admin/rotation/strategies (doc 04 §2.5, doc 07 §8): the strategy name, its selection
// semantics, its O(1) hot-path mechanism, and its strategy_params schema.
type StrategyInfo struct {
	Name      string   `json:"name"`
	Semantics string   `json:"semantics"`
	Mechanism string   `json:"mechanism"`
	Params    []string `json:"params"` // param keys accepted in key_pools.strategy_params
}

// Strategies is the 12-strategy catalog (doc 07 §8), the single source served to the UI so the
// pool editor never hard-codes it. Order is stable (documentation order).
var Strategies = []StrategyInfo{
	{"round_robin", "uniform cycle over available pool members", "atomic uint64 index mod ring size", nil},
	{"least_used", "prefer the key with the lowest recent usage", "16-bucket banding by usage EWMA; round-robin within best bucket; 1s re-band", []string{"window_s"}},
	{"weighted", "draws proportional to provider_keys.weight", "alias-method table; O(1) two-probe draw; rebuilt on change", nil},
	{"credit_based", "prefer keys with the most credits_remaining; starve near-empty keys", "16-bucket banding by remaining credits", []string{"reserve_floor"}},
	{"region_based", "route to the key sub-pool matching the request region, inner strategy within", "map region -> sub-ring, inner strategy per sub-ring", []string{"fallback_region", "inner_strategy"}},
	{"lowest_latency", "prefer the lowest latency_ewma_ms", "16-bucket banding by latency EWMA", []string{"window_s"}},
	{"highest_success", "prefer the highest success_ewma", "16-bucket banding by success EWMA", nil},
	{"ai_routing", "Beta-Thompson bandit across keys — explores under uncertainty, exploits winners", "per-key Beta posteriors (ADR-0008) sampled by the 1s re-band; hot-path pick from best bucket", []string{"prior_alpha", "prior_beta"}},
	{"random", "uniform random member", "math/rand/v2 index", nil},
	{"priority", "strict priority order (provider_keys.priority), skip unavailable", "ordered walk with per-key atomic availability", nil},
	{"failover", "primary serves until unhealthy, then next; optional automatic failback", "ordered walk; availability flipped by the KM-3 state machine", []string{"failback"}},
	{"overflow", "primary serves until a rate/quota threshold, excess spills to the next key", "ordered walk gated by the per-key lease token bucket", []string{"spill_threshold_pct"}},
}

// validStrategy reports whether name is one of the 12 catalog strategies.
func validStrategy(name string) bool {
	for _, s := range Strategies {
		if s.Name == name {
			return true
		}
	}
	return false
}

// triggerKinds is the CLOSED rotation_triggers.trigger vocabulary (doc 07 §9 error-class mapping):
// the auto-replacement thresholds an operator may tune. AUTH handling is present but may never be
// disabled (a possibly-compromised key must always park) — enforced in the PUT validator.
var triggerKinds = map[string]bool{
	"quota":      true, // QUOTA / 402 -> exhausted (auto re-enable probe)
	"rate_limit": true, // sustained RATE_LIMIT / 429 -> rate_limited (cooldown)
	"auth":       true, // AUTH / 401 -> auth_failed -> disabled (manual re-enable only)
	"timeout":    true, // timeout count over threshold -> paused pending health
}

func validTriggerKind(name string) bool { return triggerKinds[name] }
