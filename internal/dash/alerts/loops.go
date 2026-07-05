package alerts

import (
	"context"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// Loop cadences (doc 10 §5: 30s evaluation; §5.6: ~5s notifier claim interval). Zero values fall
// back to these production defaults.
type LoopConfig struct {
	EvalInterval   time.Duration
	NotifyInterval time.Duration
}

func (c LoopConfig) withDefaults() LoopConfig {
	if c.EvalInterval <= 0 {
		c.EvalInterval = 30 * time.Second
	}
	if c.NotifyInterval <= 0 {
		c.NotifyInterval = 5 * time.Second
	}
	return c
}

// Loops is the alerts background chassis: the 30s leader-elected evaluator and the notifier drain
// loop, each under its OWN per-loop advisory lock (doc 11 §1) so evaluation and delivery elect
// independently. Clean shutdown via Stop (context-cancel + WaitGroup join + lock release).
type Loops struct {
	pool     *pg.Pool
	eval     *Evaluator
	notifier *Notifier
	cfg      LoopConfig

	mu       sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	evalLock *leaderLock
	notiLock *leaderLock
}

// NewLoops assembles the chassis over store's pool.
func NewLoops(store *db.Store, eval *Evaluator, notifier *Notifier, cfg LoopConfig) *Loops {
	return &Loops{pool: store.Pool(), eval: eval, notifier: notifier, cfg: cfg.withDefaults()}
}

// Start launches the evaluator + notifier supervisor goroutines. Safe to call once.
func (l *Loops) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	l.cancel = cancel
	l.mu.Unlock()

	l.wg.Add(2)
	go l.superviseEval(runCtx)
	go l.superviseNotify(runCtx)
}

// Stop cancels the loops, releases both advisory locks, and waits for the goroutines to exit.
func (l *Loops) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	l.wg.Wait()
	l.mu.Lock()
	l.evalLock.release()
	l.notiLock.release()
	l.evalLock, l.notiLock = nil, nil
	l.mu.Unlock()
}

func (l *Loops) superviseEval(ctx context.Context) {
	defer l.wg.Done()
	t := time.NewTicker(l.cfg.EvalInterval)
	defer t.Stop()
	l.runEval(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.runEval(ctx)
		}
	}
}

func (l *Loops) runEval(ctx context.Context) {
	lk, ok := l.acquire(ctx, &l.evalLock, "dash_alert_evaluator")
	if !ok || lk == nil {
		return
	}
	_ = l.eval.EvaluateOnce(ctx)
}

func (l *Loops) superviseNotify(ctx context.Context) {
	defer l.wg.Done()
	t := time.NewTicker(l.cfg.NotifyInterval)
	defer t.Stop()
	l.runNotify(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.runNotify(ctx)
		}
	}
}

func (l *Loops) runNotify(ctx context.Context) {
	lk, ok := l.acquire(ctx, &l.notiLock, "dash_alert_notifier")
	if !ok || lk == nil {
		return
	}
	_ = l.notifier.DeliverOnce(ctx)
}

// acquire lazily takes the named advisory lock into *slot (once), returning ok=true iff this
// instance now holds it. A follower gets ok=false and simply skips the cycle, retrying next tick.
func (l *Loops) acquire(ctx context.Context, slot **leaderLock, name string) (*leaderLock, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if *slot != nil {
		return *slot, true
	}
	lk, ok := tryLeaderLock(ctx, l.pool, name)
	if !ok {
		return nil, false
	}
	*slot = lk
	return lk, true
}

// leaderLock holds a session-level advisory lock on a dedicated pooled connection.
type leaderLock struct {
	pool *pg.Pool
	conn *pg.Conn
}

// tryLeaderLock attempts pg_try_advisory_lock(hashtext(name)) on a dedicated connection.
func tryLeaderLock(ctx context.Context, pool *pg.Pool, name string) (*leaderLock, bool) {
	c, err := pool.Get(ctx)
	if err != nil {
		return nil, false
	}
	res, err := c.QueryParams("select pg_try_advisory_lock(hashtext($1))", name)
	if err != nil {
		pool.Put(c, true)
		return nil, false
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil || *res.Rows[0][0] != "t" {
		pool.Put(c, false)
		return nil, false
	}
	return &leaderLock{pool: pool, conn: c}, true
}

// release unlocks and returns the connection to the pool.
func (l *leaderLock) release() {
	if l == nil || l.conn == nil {
		return
	}
	_ = l.conn.Exec("select pg_advisory_unlock_all()")
	l.pool.Put(l.conn, false)
	l.conn = nil
}
