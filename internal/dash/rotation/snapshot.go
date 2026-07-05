package rotation

import "context"

// Snapshot is the live in-memory PoolState image served by GET /v1/admin/key-pools/{id}/
// selection-state (doc 04 §2.5) for debugging. It exposes the bands, round-robin index, and
// per-key availability + attribution counters — never any secret.
type Snapshot struct {
	Selector  string         `json:"selector"`
	PoolID    string         `json:"pool_id"`
	Strategy  string         `json:"strategy"`
	KeyCount  int            `json:"key_count"`
	RingIndex uint64         `json:"ring_index"`
	Keys      []KeySnapshot  `json:"keys"`
	Bands     []BandSnapshot `json:"bands,omitempty"`
}

// KeySnapshot is one member key's live selection state.
type KeySnapshot struct {
	KeyID            string  `json:"key_id"`
	Available        bool    `json:"available"`
	Status           string  `json:"status"`
	Weight           int     `json:"weight"`
	Priority         *int64  `json:"priority"`
	Region           string  `json:"region,omitempty"`
	LatencyEWMA      float64 `json:"latency_ewma_ms"`
	SuccessEWMA      float64 `json:"success_ewma"`
	CreditsRemaining int64   `json:"credits_remaining"`
	Calls            int64   `json:"calls"`
	OKs              int64   `json:"oks"`
	Leases           int64   `json:"leases"`
}

// BandSnapshot is one approximate-priority band's membership (band 0 = best).
type BandSnapshot struct {
	Band   int      `json:"band"`
	KeyIDs []string `json:"key_ids"`
}

// SimResult is the POST /v1/admin/key-pools/{id}/simulate response: a dry-run selection histogram.
type SimResult struct {
	Selector string         `json:"selector"`
	Strategy string         `json:"strategy"`
	N        int            `json:"n"`
	Region   string         `json:"region,omitempty"`
	Counts   map[string]int `json:"counts"`
}

// maxSimulate bounds a simulate request's draw count.
const maxSimulate = 100000

// snapshot renders the live PoolState.
func (ps *PoolState) snapshot(poolID string) Snapshot {
	snap := Snapshot{
		Selector:  ps.selector,
		PoolID:    poolID,
		Strategy:  ps.strategy,
		KeyCount:  len(ps.keys),
		RingIndex: ps.rr.Load(),
	}
	for _, k := range ps.keys {
		snap.Keys = append(snap.Keys, KeySnapshot{
			KeyID:            k.id,
			Available:        k.avail.Load(),
			Status:           k.statusStr(),
			Weight:           k.weight,
			Priority:         k.priority,
			Region:           k.region,
			LatencyEWMA:      k.latEWMA(),
			SuccessEWMA:      k.succEWMA(),
			CreditsRemaining: k.credits.Load(),
			Calls:            k.calls.Load(),
			OKs:              k.oks.Load(),
			Leases:           k.leases.Load(),
		})
	}
	if bs := ps.bands.Load(); bs != nil {
		for b := 0; b < numBands; b++ {
			if len(bs.buckets[b]) == 0 {
				continue
			}
			ids := make([]string, 0, len(bs.buckets[b]))
			for _, k := range bs.buckets[b] {
				ids = append(ids, k.id)
			}
			snap.Bands = append(snap.Bands, BandSnapshot{Band: b, KeyIDs: ids})
		}
	}
	return snap
}

// SelectionState returns the live in-memory PoolState snapshot for a pool id, building the state on
// a cache miss. Read-only.
func (e *Engine) SelectionState(ctx context.Context, poolID string) (Snapshot, error) {
	data, found, err := e.store.LoadPoolByID(ctx, poolID)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		return Snapshot{}, ErrPoolNotFound
	}
	ps, err := e.pool(ctx, data.Selector)
	if err != nil {
		return Snapshot{}, err
	}
	return ps.snapshot(poolID), nil
}

// Simulate draws n selections against a FRESH throwaway PoolState built from the pool's current
// rows, with ZERO side effects (no leases, no secret opens, no DB writes beyond the single pool
// read) and ZERO egress. It returns the per-key selection histogram.
func (e *Engine) Simulate(ctx context.Context, poolID string, n int, region string) (SimResult, error) {
	if n <= 0 {
		n = 1
	}
	if n > maxSimulate {
		n = maxSimulate
	}
	data, found, err := e.store.LoadPoolByID(ctx, poolID)
	if err != nil {
		return SimResult{}, err
	}
	if !found {
		return SimResult{}, ErrPoolNotFound
	}
	ps := buildPoolState(data.Selector, data.Strategy, data.Params, data.Rows, e.bandit)
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		k, ok := ps.Select(region)
		if !ok {
			counts["_no_key_available"]++
			continue
		}
		counts[k.id]++
	}
	return SimResult{Selector: ps.selector, Strategy: ps.strategy, N: n, Region: region, Counts: counts}, nil
}
