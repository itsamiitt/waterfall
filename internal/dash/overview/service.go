package overview

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/realtime"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
)

// Advisory lock electing the single overview tile aggregator across dashboardd instances
// (doc 02 §2.5 / ADR-0019: compute once per tick, fan out per instance).
const (
	overviewLockSQL   = "select pg_try_advisory_lock(hashtext('dash_overview_agg'))"
	overviewUnlockSQL = "select pg_advisory_unlock(hashtext('dash_overview_agg'))"

	snapshotKey = "overview_snapshot"
)

// Config tunes the aggregator. Zero values fall back to doc 02 defaults (2s tick; degradation
// to a wider tick is a deploy-time change, doc 11 §5).
type Config struct {
	TickInterval time.Duration
	Now          func() time.Time
}

func (c Config) withDefaults() Config {
	if c.TickInterval <= 0 {
		c.TickInterval = 2 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Snapshot is the GET /v1/admin/overview response body — also exactly the persisted
// self_monitor payload and the overview.tiles.tick envelope payload, which is what makes
// snapshot-then-delta convergence structural (P7 acceptance #3).
type Snapshot struct {
	GeneratedAt string `json:"generated_at"`
	Tiles       Tiles  `json:"tiles"`
}

// Aggregator is the leader-elected 2s tile loop: on the leader each tick computes the 19
// tiles (ComputeTiles), persists them to self_monitor('overview_snapshot') with a DB-side seq
// increment, and retains the snapshot in memory; followers serve the persisted row. The
// realtime poller on EVERY instance turns the seq bump into overview.tiles.tick — the hub
// publish path of ADR-0019's diagram.
type Aggregator struct {
	store   *db.Store
	selfmon *realtime.SelfMon
	cfg     Config
	log     *slog.Logger

	leaderG *metrics.Gauge

	mu       sync.Mutex
	leadConn *pg.Conn
	leader   bool
	lastSnap []byte // leader's in-memory last tick (serves GET /overview without recompute)
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewAggregator builds the loop. reg/log may be nil.
func NewAggregator(store *db.Store, selfmon *realtime.SelfMon, cfg Config, reg *metrics.Registry, log *slog.Logger) *Aggregator {
	if reg == nil {
		reg = metrics.New()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Aggregator{
		store: store, selfmon: selfmon, cfg: cfg.withDefaults(), log: log,
		leaderG: reg.Gauge("dash_overview_leader", "1 iff this instance holds the dash_overview_agg advisory lock"),
	}
}

// Start launches the tick loop (leadership attempted every tick until acquired).
func (a *Aggregator) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.done = make(chan struct{})
	done := a.done
	a.mu.Unlock()
	go func() {
		defer close(done)
		t := time.NewTicker(a.cfg.TickInterval)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				a.tick(runCtx)
			}
		}
	}()
}

// Stop cancels the loop and releases leadership (the advisory lock also releases on
// connection close, so a crashed leader frees the lock without cooperating).
func (a *Aggregator) Stop() {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.leadConn != nil {
		_ = a.leadConn.Exec(overviewUnlockSQL)
		a.store.Pool().Put(a.leadConn, false)
		a.leadConn = nil
	}
	a.leader = false
	a.leaderG.Set(0)
}

// Leader reports whether this instance currently holds the overview lock.
func (a *Aggregator) Leader() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.leader
}

func (a *Aggregator) tick(ctx context.Context) {
	if !a.Leader() {
		a.tryBecomeLeader(ctx)
		if !a.Leader() {
			return
		}
	}
	now := a.cfg.Now().UTC()
	ages, err := a.selfmon.HeartbeatAges(ctx, []string{"fold:usage", snapshotKey, "queue_stats_sample"})
	if err != nil {
		a.log.Warn("overview heartbeat ages", "err", err)
		ages = nil
	}
	tiles, err := ComputeTilesTx(ctx, a.store, now, ages)
	if err != nil {
		a.log.Warn("overview tile compute", "err", err)
		return
	}
	payload, err := json.Marshal(Snapshot{GeneratedAt: now.Format(time.RFC3339), Tiles: tiles})
	if err != nil {
		a.log.Warn("overview snapshot marshal", "err", err)
		return
	}
	if _, err := a.selfmon.UpsertSnapshot(ctx, snapshotKey, "overview", payload); err != nil {
		a.log.Warn("overview snapshot persist", "err", err)
		return
	}
	a.mu.Lock()
	a.lastSnap = payload
	a.mu.Unlock()
}

func (a *Aggregator) tryBecomeLeader(ctx context.Context) {
	c, err := a.store.Pool().Get(ctx)
	if err != nil {
		return
	}
	res, err := c.Query(overviewLockSQL)
	if err != nil {
		a.store.Pool().Put(c, true)
		return
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil || *res.Rows[0][0] != "t" {
		a.store.Pool().Put(c, false)
		return
	}
	a.mu.Lock()
	a.leadConn = c
	a.leader = true
	a.mu.Unlock()
	a.leaderG.Set(1)
}

// Snapshot serves GET /v1/admin/overview: the leader's in-memory last tick, a follower's
// persisted self_monitor row, or — before the first tick ever lands (fresh boot) — one inline
// computation (the aggregator always returns tiles; doc 09 §1.4).
func (a *Aggregator) Snapshot(ctx context.Context) ([]byte, error) {
	a.mu.Lock()
	if a.leader && a.lastSnap != nil {
		snap := a.lastSnap
		a.mu.Unlock()
		return snap, nil
	}
	a.mu.Unlock()

	payload, _, _, found, err := a.selfmon.Snapshot(ctx, snapshotKey)
	if err != nil {
		return nil, err
	}
	if found && len(payload) > 0 {
		return payload, nil
	}
	now := a.cfg.Now().UTC()
	tiles, err := ComputeTilesTx(ctx, a.store, now, nil)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Snapshot{GeneratedAt: now.Format(time.RFC3339), Tiles: tiles})
}
