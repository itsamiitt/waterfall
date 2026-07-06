package rotation

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"sort"
	"sync/atomic"
	"time"

	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// banditField is the fixed Field the ai_routing strategy learns per key: the bandit is reused
// keyed by (key_id, banditField) so each Provider Key gets its own Beta posterior over
// "did a lease on this key succeed?" (doc 07 §8 ai_routing, ADR-0008 machinery reused).
const banditField = domain.Field("_lease")

// numBands is the fixed 16-bucket approximate-priority banding used by the score-driven
// strategies (least_used / lowest_latency / highest_success / credit_based / ai_routing). The hot
// path only picks from the best non-empty bucket — it never scans or sorts (doc 07 §8, MASTER §5).
const numBands = 16

// keyState is one Provider Key's live selection state. The immutable identity/config fields are
// set once at build; the atomics carry everything the hot path and the 1s re-band loop touch, so
// selection stays lock-free.
type keyState struct {
	id          string
	envelopeID  string
	weight      int
	priority    *int64
	region      string
	dailyLimit  int64 // provider_keys.daily_limit for the lease bucket (0 = unlimited)
	costPerCall int64 // provider_keys.cost_per_call — credits attributed to one leased call (OI-P4-1)

	avail  atomic.Bool            // KM-3 availability (KeyAvailable of the live status)
	status atomic.Pointer[string] // live status string for the selection-state snapshot

	// Score inputs, updated lock-free in Lease.Done (observe) and read by the re-band loop. Stored
	// as float64 bits so concurrent updates are data-race-free (benign lost EWMA updates only).
	latBits   atomic.Uint64 // EWMA latency ms (lower is better)
	succBits  atomic.Uint64 // EWMA success rate in [0,1] (higher is better)
	usageBits atomic.Uint64 // decaying usage counter (lower is better for least_used)
	credits   atomic.Int64  // credits_remaining (higher is better for credit_based)

	// Attribution counters (G5): every leased call is counted here by key_id.
	calls  atomic.Int64
	oks    atomic.Int64
	leases atomic.Int64

	// rlStreak counts consecutive RATE_LIMIT outcomes; the engine transitions the key to
	// rate_limited only once the streak reaches the "sustained" threshold (doc 07 §9).
	rlStreak atomic.Int64
}

func (k *keyState) latEWMA() float64  { return math.Float64frombits(k.latBits.Load()) }
func (k *keyState) succEWMA() float64 { return math.Float64frombits(k.succBits.Load()) }
func (k *keyState) usage() float64    { return math.Float64frombits(k.usageBits.Load()) }

func (k *keyState) statusStr() string {
	if p := k.status.Load(); p != nil {
		return *p
	}
	return ""
}

// markStatus updates the live status snapshot and recomputes availability (KM-3). It is the single
// place a status change flips the hot-path availability bit.
func (k *keyState) markStatus(s string) {
	k.status.Store(&s)
	k.avail.Store(KeyAvailable(s))
}

// observe folds one call Outcome into the key's EWMA / usage / attribution counters. Called from
// Lease.Done, off the selection hot path. Lock-free; updates are atomic (no data race).
func (k *keyState) observe(o provider.Outcome) {
	const alpha = 0.2
	k.calls.Add(1)
	if o.OK {
		k.oks.Add(1)
	}
	lat := k.latEWMA()
	if lat == 0 {
		lat = float64(o.LatencyMs)
	} else {
		lat = (1-alpha)*lat + alpha*float64(o.LatencyMs)
	}
	k.latBits.Store(math.Float64bits(lat))

	obs := 0.0
	if o.OK {
		obs = 1.0
	}
	k.succBits.Store(math.Float64bits((1-alpha)*k.succEWMA() + alpha*obs))
	k.usageBits.Store(math.Float64bits(k.usage()*0.9 + 1))
}

// bandSet is an immutable snapshot of the 16 approximate-priority buckets (0 = best). It is swapped
// wholesale via atomic.Pointer by the re-band loop, so the hot path never locks. The per-bucket
// cursors reset each re-band (once per second) — acceptable for round-robin-within-bucket fairness.
type bandSet struct {
	buckets [numBands][]*keyState
	cursors [numBands]atomic.Uint64
}

// regionRing is one region's sub-pool for region_based selection, with its own round-robin cursor.
type regionRing struct {
	keys  []*keyState
	rr    atomic.Uint64
	inner string
}

// poolParams is the parsed key_pools.strategy_params (doc 07 §8 per-strategy schema). Absent keys
// take documented defaults.
type poolParams struct {
	windowS        int
	reserveFloor   int64
	spillPct       int
	fallbackRegion string
	innerStrategy  string
	priorAlpha     float64
	priorBeta      float64
}

func parseParams(raw string) poolParams {
	p := poolParams{windowS: 300, spillPct: 80, fallbackRegion: "us", innerStrategy: "round_robin", priorAlpha: 1, priorBeta: 1}
	if raw == "" {
		return p
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return p
	}
	if v, ok := m["window_s"].(float64); ok {
		p.windowS = int(v)
	}
	if v, ok := m["reserve_floor"].(float64); ok {
		p.reserveFloor = int64(v)
	}
	if v, ok := m["spill_threshold_pct"].(float64); ok {
		p.spillPct = int(v)
	}
	if v, ok := m["fallback_region"].(string); ok && v != "" {
		p.fallbackRegion = v
	}
	if v, ok := m["inner_strategy"].(string); ok && v != "" {
		p.innerStrategy = v
	}
	if v, ok := m["prior_alpha"].(float64); ok {
		p.priorAlpha = v
	}
	if v, ok := m["prior_beta"].(float64); ok {
		p.priorBeta = v
	}
	return p
}

// PoolState is one Key Pool's in-memory selection state, rebuilt on demand / periodic refresh (and
// on the P3 config-epoch Invalidate hook). The keys slice is immutable after build (index-stable);
// only the atomics and the banded snapshot mutate, so Select is lock-free and O(1) / bounded.
type PoolState struct {
	selector string
	strategy string
	params   poolParams

	keys []*keyState
	byID map[string]*keyState

	rr    atomic.Uint64 // round-robin cursor for index strategies
	alias *aliasTable   // weighted (immutable; a weight change rebuilds the PoolState)

	bands atomic.Pointer[bandSet] // score / ai_routing strategies
	rings map[string]*regionRing  // region_based

	bandit   *bandit.Bandit
	rawScore func(k *keyState, sc *bandit.Scorer) float64 // higher = better; nil for non-banded
}

// buildPoolState assembles a PoolState from a pool's rows. rows should be ordered by (priority
// NULLS LAST, id) so priority/failover/overflow ordered walks are deterministic.
func buildPoolState(selector, strategy, params string, rows []poolKeyRow, bnd *bandit.Bandit) *PoolState {
	sortRowsForOrdering(rows) // deterministic ordered-walk order even if the caller did not pre-sort
	ps := &PoolState{
		selector: selector,
		strategy: strategy,
		params:   parseParams(params),
		byID:     make(map[string]*keyState, len(rows)),
		bandit:   bnd,
	}
	ps.keys = make([]*keyState, 0, len(rows))
	for _, r := range rows {
		k := &keyState{
			id:         r.ID,
			envelopeID: r.EnvelopeID,
			weight:     r.Weight,
			priority:   r.Priority,
			region:     r.Region,
		}
		if r.DailyLimit != nil {
			k.dailyLimit = *r.DailyLimit
		}
		if r.CostPerCall != nil {
			k.costPerCall = *r.CostPerCall
		}
		if r.LatencyEWMA != nil {
			k.latBits.Store(math.Float64bits(*r.LatencyEWMA))
		}
		if r.SuccessEWMA != nil {
			k.succBits.Store(math.Float64bits(*r.SuccessEWMA))
		}
		if r.CreditsRemaining != nil {
			k.credits.Store(*r.CreditsRemaining)
		}
		k.markStatus(r.Status)
		ps.keys = append(ps.keys, k)
		ps.byID[k.id] = k
	}

	switch strategy {
	case "weighted":
		w := make([]float64, len(ps.keys))
		for i, k := range ps.keys {
			w[i] = float64(k.weight)
		}
		ps.alias = newAliasTable(w)
	case "region_based":
		ps.buildRings()
	case "least_used", "lowest_latency", "highest_success", "credit_based", "ai_routing":
		ps.rawScore = scoreExtractor(strategy)
		ps.reband()
	}
	return ps
}

// buildRings groups keys by region into inner sub-rings (region_based).
func (ps *PoolState) buildRings() {
	ps.rings = map[string]*regionRing{}
	for _, k := range ps.keys {
		reg := k.region
		if reg == "" {
			reg = ps.params.fallbackRegion
		}
		ring := ps.rings[reg]
		if ring == nil {
			ring = &regionRing{inner: ps.params.innerStrategy}
			ps.rings[reg] = ring
		}
		ring.keys = append(ring.keys, k)
	}
}

// scoreExtractor returns the raw-score function for a banded strategy (higher = better). ai_routing
// samples a fresh Beta posterior per re-band via the provided scorer (exploration).
func scoreExtractor(strategy string) func(*keyState, *bandit.Scorer) float64 {
	switch strategy {
	case "lowest_latency":
		return func(k *keyState, _ *bandit.Scorer) float64 { return -k.latEWMA() }
	case "highest_success":
		return func(k *keyState, _ *bandit.Scorer) float64 { return k.succEWMA() }
	case "least_used":
		return func(k *keyState, _ *bandit.Scorer) float64 { return -k.usage() }
	case "credit_based":
		return func(k *keyState, _ *bandit.Scorer) float64 { return float64(k.credits.Load()) }
	case "ai_routing":
		return func(k *keyState, sc *bandit.Scorer) float64 {
			if sc == nil {
				return k.succEWMA()
			}
			return sc.Score(k.id, banditField, domain.Confidence(0.5))
		}
	default:
		return nil
	}
}

// reband recomputes the 16 approximate-priority buckets from current scores and swaps them in
// atomically. Runs off the hot path (build-time once + the 1s background loop). No-op for
// non-banded strategies.
func (ps *PoolState) reband() {
	if ps.rawScore == nil {
		return
	}
	var sc *bandit.Scorer
	if ps.strategy == "ai_routing" && ps.bandit != nil {
		sc = ps.bandit.NewScorer(time.Now().UnixNano())
	}
	n := len(ps.keys)
	raw := make([]float64, n)
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, k := range ps.keys {
		r := ps.rawScore(k, sc)
		raw[i] = r
		lo = math.Min(lo, r)
		hi = math.Max(hi, r)
	}
	bs := &bandSet{}
	span := hi - lo
	for i, k := range ps.keys {
		g := 0.5
		if span > 0 {
			g = (raw[i] - lo) / span
		}
		bucket := int((1 - g) * numBands)
		if bucket < 0 {
			bucket = 0
		}
		if bucket >= numBands {
			bucket = numBands - 1
		}
		bs.buckets[bucket] = append(bs.buckets[bucket], k)
	}
	ps.bands.Store(bs)
}

// --- selection (O(1) / bounded, lock-free hot path) ---

// Select returns the strategy's next key among AVAILABLE members for the request region, or
// ok=false when the pool has no available key. It is the hot-path entry point used by the
// benchmark and the simulate endpoint; the Lease loop uses selectSkip for failover.
func (ps *PoolState) Select(region string) (*keyState, bool) {
	return ps.selectSkip(region, nil)
}

// usable reports whether k may be selected now: available (KM-3) and not already tried this lease.
func usable(k *keyState, skip map[string]bool) bool {
	if !k.avail.Load() {
		return false
	}
	if skip != nil && skip[k.id] {
		return false
	}
	return true
}

// selectSkip is Select with an optional already-tried set (nil in the common single-pick path).
func (ps *PoolState) selectSkip(region string, skip map[string]bool) (*keyState, bool) {
	switch ps.strategy {
	case "random":
		return pickRandom(ps.keys, skip)
	case "weighted":
		return ps.pickWeighted(skip)
	case "priority", "failover", "overflow":
		return pickOrdered(ps.keys, skip)
	case "least_used", "lowest_latency", "highest_success", "credit_based", "ai_routing":
		return ps.pickBanded(skip)
	case "region_based":
		return ps.pickRegion(region, skip)
	default: // round_robin (and any unknown strategy falls back to it)
		return pickRoundRobin(ps.keys, &ps.rr, skip)
	}
}

// pickRoundRobin advances the cursor and returns the first available key from there (bounded walk,
// first-hit fast path).
func pickRoundRobin(ks []*keyState, rr *atomic.Uint64, skip map[string]bool) (*keyState, bool) {
	n := len(ks)
	if n == 0 {
		return nil, false
	}
	start := int(rr.Add(1) % uint64(n))
	for i := 0; i < n; i++ {
		k := ks[(start+i)%n]
		if usable(k, skip) {
			return k, true
		}
	}
	return nil, false
}

func pickRandom(ks []*keyState, skip map[string]bool) (*keyState, bool) {
	n := len(ks)
	if n == 0 {
		return nil, false
	}
	start := rand.IntN(n)
	for i := 0; i < n; i++ {
		k := ks[(start+i)%n]
		if usable(k, skip) {
			return k, true
		}
	}
	return nil, false
}

// pickOrdered walks the pre-sorted slice (priority asc / primary-first) and returns the first
// available key — this realizes priority, failover, and overflow's ordered walk.
func pickOrdered(ks []*keyState, skip map[string]bool) (*keyState, bool) {
	for _, k := range ks {
		if usable(k, skip) {
			return k, true
		}
	}
	return nil, false
}

// pickWeighted draws via the O(1) alias table and, only if that key is unusable (raced
// unavailable / tried), falls back to a bounded scan from the sampled index.
func (ps *PoolState) pickWeighted(skip map[string]bool) (*keyState, bool) {
	if ps.alias == nil {
		return pickRoundRobin(ps.keys, &ps.rr, skip)
	}
	idx := ps.alias.sample()
	if usable(ps.keys[idx], skip) {
		return ps.keys[idx], true
	}
	n := len(ps.keys)
	for i := 1; i < n; i++ {
		k := ps.keys[(idx+i)%n]
		if usable(k, skip) {
			return k, true
		}
	}
	return nil, false
}

// pickBanded returns a key from the best non-empty band, round-robin within the band.
func (ps *PoolState) pickBanded(skip map[string]bool) (*keyState, bool) {
	bs := ps.bands.Load()
	if bs == nil {
		return pickRoundRobin(ps.keys, &ps.rr, skip)
	}
	for b := 0; b < numBands; b++ {
		bucket := bs.buckets[b]
		if len(bucket) == 0 {
			continue
		}
		start := int(bs.cursors[b].Add(1) % uint64(len(bucket)))
		for i := 0; i < len(bucket); i++ {
			k := bucket[(start+i)%len(bucket)]
			if usable(k, skip) {
				return k, true
			}
		}
	}
	return nil, false
}

// pickRegion routes to the region's sub-ring (or the fallback region), then applies the inner
// strategy within it; an unmatched region falls back to a whole-pool round-robin.
func (ps *PoolState) pickRegion(region string, skip map[string]bool) (*keyState, bool) {
	ring := ps.rings[region]
	if ring == nil {
		ring = ps.rings[ps.params.fallbackRegion]
	}
	if ring == nil {
		return pickRoundRobin(ps.keys, &ps.rr, skip)
	}
	switch ring.inner {
	case "random":
		return pickRandom(ring.keys, skip)
	case "priority", "failover":
		return pickOrdered(ring.keys, skip)
	default:
		return pickRoundRobin(ring.keys, &ring.rr, skip)
	}
}

// sortRowsForOrdering orders rows by (priority ASC NULLS LAST, id) so ordered-walk strategies are
// deterministic even when the caller did not pre-sort (defensive; the store also ORDER BYs).
func sortRowsForOrdering(rows []poolKeyRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		pi, pj := rows[i].Priority, rows[j].Priority
		switch {
		case pi == nil && pj == nil:
			return rows[i].ID < rows[j].ID
		case pi == nil:
			return false
		case pj == nil:
			return true
		case *pi != *pj:
			return *pi < *pj
		default:
			return rows[i].ID < rows[j].ID
		}
	})
}
