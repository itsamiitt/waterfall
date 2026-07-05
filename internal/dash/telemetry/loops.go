package telemetry

import (
	"context"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
)

// aggregatorLockSQL elects the single cluster leader (doc 10 §2). hashtext maps the well-known
// name to the advisory-lock key; the lock is held for the leader's lifetime on a dedicated conn.
const aggregatorLockSQL = "select pg_try_advisory_lock(hashtext('dash_aggregator'))"
const aggregatorUnlockSQL = "select pg_advisory_unlock(hashtext('dash_aggregator'))"

// Leadership holds the advisory lock on a dedicated pooled connection. Release unlocks and
// returns the connection to the pool. It is the leader-election primitive behind the aggregator
// loop; loss of the leader loses no data — the next leader resumes from the fold watermark.
type Leadership struct {
	pool *pg.Pool
	conn *pg.Conn
}

// TryAcquireLeadership attempts to become the aggregator leader. ok=false means another instance
// holds the lock (this instance is a follower). The returned Leadership must be Released.
func TryAcquireLeadership(ctx context.Context, pool *pg.Pool) (*Leadership, bool, error) {
	c, err := pool.Get(ctx)
	if err != nil {
		return nil, false, err
	}
	res, err := c.Query(aggregatorLockSQL)
	if err != nil {
		pool.Put(c, true)
		return nil, false, err
	}
	if len(res.Rows) == 0 || s(res.Rows[0][0]) != "t" {
		pool.Put(c, false)
		return nil, false, nil
	}
	return &Leadership{pool: pool, conn: c}, true, nil
}

// Release unlocks the advisory lock and returns the connection to the pool.
func (l *Leadership) Release() {
	if l == nil || l.conn == nil {
		return
	}
	_ = l.conn.Exec(aggregatorUnlockSQL)
	l.pool.Put(l.conn, false)
	l.conn = nil
}

// LoopConfig tunes the background cadences (doc 10 §2: 5s fold). Zero values fall back to
// production defaults.
type LoopConfig struct {
	FoldInterval      time.Duration // rollup fold cadence (default 5s)
	MaintainInterval  time.Duration // partition ensure/detach cadence (default 6h)
	ReconcileInterval time.Duration // key-budget reconcile cadence (default 24h)
}

func (c LoopConfig) withDefaults() LoopConfig {
	if c.FoldInterval <= 0 {
		c.FoldInterval = 5 * time.Second
	}
	if c.MaintainInterval <= 0 {
		c.MaintainInterval = 6 * time.Hour
	}
	if c.ReconcileInterval <= 0 {
		c.ReconcileInterval = 24 * time.Hour
	}
	return c
}

// Loops is the dashboardd background-loop chassis (doc 12 §P4): leader-elected aggregator fold,
// partition maintainer, and key-budget reconcile, plus dead-man's-switch heartbeats. Only the
// leader runs the fold/maintain/reconcile loops; every instance may run its own EnsurePartitions
// at startup (idempotent). Clean shutdown via Stop (context-cancel + WaitGroup join).
type Loops struct {
	store *db.Store
	agg   *Aggregator
	maint *Maintainer
	recon *Reconciler
	now   func() time.Time
	cfg   LoopConfig

	leaderG *metrics.Gauge

	mu     sync.Mutex
	lead   *Leadership
	cancel context.CancelFunc
	wg     sync.WaitGroup
	leader bool
}

// NewLoops assembles the chassis. now may be nil (wall clock); reg may be nil.
func NewLoops(store *db.Store, now func() time.Time, reg *metrics.Registry, cfg LoopConfig) *Loops {
	if now == nil {
		now = time.Now
	}
	if reg == nil {
		reg = metrics.New()
	}
	return &Loops{
		store:   store,
		agg:     NewAggregator(store, now, reg),
		maint:   NewMaintainer(store, now, reg),
		recon:   NewReconciler(store),
		now:     now,
		cfg:     cfg.withDefaults(),
		leaderG: reg.Gauge("dash_aggregator_leader", "1 iff this instance holds the dash_aggregator advisory lock"),
	}
}

// Aggregator/Maintainer/Reconciler expose the components for direct calls (tests, and the read
// helpers hang off the Aggregator).
func (l *Loops) Aggregator() *Aggregator { return l.agg }
func (l *Loops) Maintainer() *Maintainer { return l.maint }
func (l *Loops) Reconciler() *Reconciler { return l.recon }

// Leader reports whether this instance currently holds leadership.
func (l *Loops) Leader() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leader
}

// Start runs the synchronous startup partition ensure (doc 03 §4), attempts leadership, and — if
// leader — launches the fold/maintain/reconcile loops. It is safe to call once. Non-leaders keep
// retrying leadership on the maintain cadence.
func (l *Loops) Start(ctx context.Context) error {
	// Ensure partitions exist before first traffic, regardless of leadership (idempotent).
	if err := l.maint.EnsurePartitions(ctx, l.now()); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	l.cancel = cancel
	l.mu.Unlock()

	l.wg.Add(1)
	go l.supervise(runCtx)
	return nil
}

// Stop cancels the loops, releases leadership, and waits for goroutines to exit.
func (l *Loops) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	l.wg.Wait()
	l.mu.Lock()
	if l.lead != nil {
		l.lead.Release()
		l.lead = nil
	}
	l.leader = false
	l.leaderG.Set(0)
	l.mu.Unlock()
}

// supervise acquires leadership (retrying) and drives the leader loops until ctx is cancelled.
func (l *Loops) supervise(ctx context.Context) {
	defer l.wg.Done()
	fold := time.NewTicker(l.cfg.FoldInterval)
	maintain := time.NewTicker(l.cfg.MaintainInterval)
	reconcile := time.NewTicker(l.cfg.ReconcileInterval)
	defer fold.Stop()
	defer maintain.Stop()
	defer reconcile.Stop()

	l.tryBecomeLeader(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-fold.C:
			if l.Leader() {
				_, _ = l.agg.Fold(ctx)
			} else {
				l.tryBecomeLeader(ctx)
			}
		case <-maintain.C:
			if l.Leader() {
				_ = l.maint.EnsurePartitions(ctx, l.now())
				_, _ = l.maint.DetachExpired(ctx, l.now())
			} else {
				l.tryBecomeLeader(ctx)
			}
		case <-reconcile.C:
			if l.Leader() {
				// Reconcile the day that just closed (UTC).
				_, _ = l.recon.ReconcileKeyBudgets(ctx, l.now().Add(-12*time.Hour))
			}
		}
	}
}

func (l *Loops) tryBecomeLeader(ctx context.Context) {
	l.mu.Lock()
	already := l.leader
	l.mu.Unlock()
	if already {
		return
	}
	lead, ok, err := TryAcquireLeadership(ctx, l.store.Pool())
	if err != nil || !ok {
		return
	}
	l.mu.Lock()
	l.lead = lead
	l.leader = true
	l.mu.Unlock()
	l.leaderG.Set(1)
}
