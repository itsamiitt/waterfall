package rotation

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Sentinel errors (wrapped with %w by the HTTP layer). None carries key material.
var (
	// ErrPoolNotFound reports no key_pool for the selector / id.
	ErrPoolNotFound = errors.New("rotation: key pool not found")
	// ErrNoKeyAvailable reports the pool has no available key to lease (all parked / quota-exhausted).
	ErrNoKeyAvailable = errors.New("rotation: no available key in pool")
	// ErrSecretOpen reports that the leased key's envelope could not be opened. It deliberately
	// carries no envelope id or crypto detail.
	ErrSecretOpen = errors.New("rotation: could not open key secret")
)

// SecretOpener opens a sealed envelope into its plaintext secret. Satisfied by NewSecretOpener over
// a secrets.Backend. The Engine holds the secret only long enough to hand it to the egress
// injector; it is never logged.
type SecretOpener interface {
	Open(ctx context.Context, envelopeID string) (string, error)
}

// backendOpener adapts secrets.Backend to SecretOpener.
type backendOpener struct{ b secrets.Backend }

// NewSecretOpener adapts a secrets.Backend into the rotation SecretOpener seam.
func NewSecretOpener(b secrets.Backend) SecretOpener { return backendOpener{b: b} }

func (o backendOpener) Open(ctx context.Context, envelopeID string) (string, error) {
	pt, err := o.b.Open(ctx, secrets.EnvelopeID(envelopeID))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// Config bundles the Engine's collaborators.
type Config struct {
	Store              Store
	Audit              Auditor
	Secrets            SecretOpener
	Bandit             *bandit.Bandit
	Sink               EventSink
	Now                func() time.Time
	Logger             *slog.Logger
	RateLimitThreshold int           // sustained RATE_LIMIT count before parking; default 3
	RebandEvery        time.Duration // score re-band cadence; default 1s
	RefreshEvery       time.Duration // pool reload cadence; default 30s
}

// Engine is the rotation LeaseResolver: it selects a key per the pool strategy, draws a batched
// quota lease, opens the secret, and returns a Lease whose Done folds the Outcome back into EWMA /
// bandit / the KM-3 trigger state machine. It also satisfies provider.KeyResolver (Resolve = Lease
// then discard Done) for back-compat call sites.
type Engine struct {
	store   Store
	audit   Auditor
	secrets SecretOpener
	bandit  *bandit.Bandit
	sink    EventSink
	now     func() time.Time
	log     *slog.Logger

	buckets  *bucketRegistry
	trigger  *Trigger
	rlThresh int64

	mu    sync.RWMutex
	pools map[string]*PoolState

	rebandEvery  time.Duration
	refreshEvery time.Duration
	stop         chan struct{}
	stopOnce     sync.Once
}

var (
	_ provider.LeaseResolver = (*Engine)(nil)
	_ provider.KeyResolver   = (*Engine)(nil)
)

// New builds an Engine from cfg, applying defaults.
func New(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Sink == nil {
		cfg.Sink = &CollectingSink{}
	}
	if cfg.Bandit == nil {
		cfg.Bandit = bandit.New()
	}
	if cfg.RateLimitThreshold <= 0 {
		cfg.RateLimitThreshold = 3
	}
	if cfg.RebandEvery <= 0 {
		cfg.RebandEvery = time.Second
	}
	if cfg.RefreshEvery <= 0 {
		cfg.RefreshEvery = 30 * time.Second
	}
	return &Engine{
		store:        cfg.Store,
		audit:        cfg.Audit,
		secrets:      cfg.Secrets,
		bandit:       cfg.Bandit,
		sink:         cfg.Sink,
		now:          cfg.Now,
		log:          cfg.Logger,
		buckets:      newBucketRegistry(cfg.Store),
		trigger:      newTrigger(cfg.Store, cfg.Audit, cfg.Sink, cfg.Now, cfg.Logger),
		rlThresh:     int64(cfg.RateLimitThreshold),
		pools:        map[string]*PoolState{},
		rebandEvery:  cfg.RebandEvery,
		refreshEvery: cfg.RefreshEvery,
		stop:         make(chan struct{}),
	}
}

// --- provider.LeaseResolver / provider.KeyResolver ---

// Lease selects a key from the pool's strategy, draws one quota token, opens its secret, and returns
// a Lease attributed to the key_id. On a per-key quota exhaustion it fires the KM-3 QUOTA ->
// exhausted transition and fails over to the next key (bounded by pool size). It never logs secrets.
func (e *Engine) Lease(ctx context.Context, poolSelector string) (provider.Lease, error) {
	ps, err := e.pool(ctx, poolSelector)
	if err != nil {
		return provider.Lease{}, err
	}
	region := regionFromContext(ctx)
	var skip map[string]bool
	// Bound the failover walk by the pool size (+1 for the first pick).
	for attempt := 0; attempt <= len(ps.keys); attempt++ {
		k, ok := ps.selectSkip(region, skip)
		if !ok {
			return provider.Lease{}, ErrNoKeyAvailable
		}
		if err := e.buckets.draw(ctx, k.id, k.dailyLimit); err != nil {
			if errors.Is(err, ErrQuotaExhausted) {
				e.fireQuota(ctx, k)
				if skip == nil {
					skip = make(map[string]bool, 2)
				}
				skip[k.id] = true
				continue
			}
			return provider.Lease{}, err
		}
		secret, oerr := e.secrets.Open(ctx, k.envelopeID)
		if oerr != nil {
			e.log.Error("rotation: open secret failed", "key_id", k.id, "err", oerr)
			return provider.Lease{}, ErrSecretOpen
		}
		k.leases.Add(1)
		kk := k
		return provider.Lease{
			KeyID:  kk.id,
			Secret: secret,
			Done:   func(o provider.Outcome) { e.recordOutcome(ctx, kk, o) },
		}, nil
	}
	return provider.Lease{}, ErrNoKeyAvailable
}

// Resolve satisfies provider.KeyResolver for back-compat call sites: it draws a lease and returns
// the secret, DISCARDING the Done callback (no outcome feedback). Used only where the caller does
// not classify the response (StaticKeyResolver-style sites); the egress AuthInjector prefers Lease.
func (e *Engine) Resolve(poolSelector string) (string, error) {
	lease, err := e.Lease(context.Background(), poolSelector)
	if err != nil {
		return "", err
	}
	return lease.Secret, nil
}

// recordOutcome folds one call Outcome back into the key: EWMA/attribution, the ai_routing
// posterior, and the KM-3 trigger state machine (AUTH -> auth_failed -> disabled + alert,
// QUOTA -> exhausted, sustained RATE_LIMIT -> rate_limited). Runs on the egress path (Lease.Done).
func (e *Engine) recordOutcome(ctx context.Context, k *keyState, o provider.Outcome) {
	k.observe(o)
	if e.bandit != nil {
		e.bandit.Update(k.id, banditField, o.OK)
	}
	if o.OK {
		k.rlStreak.Store(0)
		return
	}
	from := State(k.statusStr())
	switch o.Class {
	case domain.ClassAuth:
		// active -> auth_failed -> disabled (+alert). A possibly-compromised key never self-heals.
		if e.trigger.Apply(ctx, k.id, from, StateAuthFailed, o.Class.String(), true) == nil {
			if e.trigger.Apply(ctx, k.id, StateAuthFailed, StateDisabled, o.Class.String(), true) == nil {
				k.markStatus(string(StateDisabled))
			} else {
				k.markStatus(string(StateAuthFailed))
			}
		}
	case domain.ClassQuota:
		if e.trigger.Apply(ctx, k.id, from, StateExhausted, o.Class.String(), false) == nil {
			k.markStatus(string(StateExhausted))
		}
	case domain.ClassRateLimit:
		if k.rlStreak.Add(1) >= e.rlThresh {
			if e.trigger.Apply(ctx, k.id, from, StateRateLimited, o.Class.String(), false) == nil {
				k.markStatus(string(StateRateLimited))
				k.rlStreak.Store(0)
			}
		}
	default:
		// TRANSIENT / PROVIDER_DOWN / NOT_FOUND / BAD_REQUEST / UNKNOWN: no key transition
		// (PROVIDER_DOWN opens the provider breaker, orthogonal to key status — doc 07 §9).
	}
}

// fireQuota parks a key that just failed its quota draw (active -> exhausted), then removes it from
// local rotation. When the current state cannot legally reach exhausted it is still taken out of
// in-memory rotation for this lease so failover proceeds.
func (e *Engine) fireQuota(ctx context.Context, k *keyState) {
	from := State(k.statusStr())
	if e.trigger.Apply(ctx, k.id, from, StateExhausted, domain.ClassQuota.String(), false) == nil {
		k.markStatus(string(StateExhausted))
		return
	}
	k.avail.Store(false)
}

// --- pool cache ---

// pool returns the cached PoolState for selector, loading + building it on a miss.
func (e *Engine) pool(ctx context.Context, selector string) (*PoolState, error) {
	e.mu.RLock()
	ps := e.pools[selector]
	e.mu.RUnlock()
	if ps != nil {
		return ps, nil
	}
	data, found, err := e.store.LoadPoolBySelector(ctx, selector)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrPoolNotFound
	}
	built := buildPoolState(data.Selector, data.Strategy, data.Params, data.Rows, e.bandit)
	e.mu.Lock()
	if existing := e.pools[selector]; existing != nil {
		built = existing // lost the build race; keep the shared instance
	} else {
		e.pools[selector] = built
	}
	e.mu.Unlock()
	return built, nil
}

// Invalidate drops the cached PoolState for a selector so the next Lease reloads it from the pool /
// key rows. This is the hook the P3 config-epoch watcher (OI-KEYS-4) will call on a key_pool epoch
// bump; for P2 it is also called after a rotation shifts membership. Rebuild-on-demand + the
// periodic refresh loop stand in for epoch-driven invalidation until P3 lands.
func (e *Engine) Invalidate(selector string) {
	e.mu.Lock()
	delete(e.pools, selector)
	e.mu.Unlock()
}

// Start launches the background re-band loop (score strategies, doc 07 §8 "re-banded by a 1s
// background loop, NEVER on the hot path") and the pool refresh loop. It is a no-op to call the
// selection path without Start — build-time banding keeps score strategies correct immediately.
func (e *Engine) Start() {
	go e.rebandLoop()
	go e.refreshLoop()
}

// Stop halts the background loops.
func (e *Engine) Stop() { e.stopOnce.Do(func() { close(e.stop) }) }

func (e *Engine) rebandLoop() {
	t := time.NewTicker(e.rebandEvery)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.mu.RLock()
			pools := make([]*PoolState, 0, len(e.pools))
			for _, ps := range e.pools {
				pools = append(pools, ps)
			}
			e.mu.RUnlock()
			for _, ps := range pools {
				ps.reband()
			}
		}
	}
}

func (e *Engine) refreshLoop() {
	t := time.NewTicker(e.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.mu.RLock()
			selectors := make([]string, 0, len(e.pools))
			for sel := range e.pools {
				selectors = append(selectors, sel)
			}
			e.mu.RUnlock()
			for _, sel := range selectors {
				e.refreshPool(sel)
			}
		}
	}
}

// refreshPool reloads a pool's rows and rebuilds its PoolState, picking up membership + authoritative
// status changes. In-memory EWMA / attribution counters reset to the DB values on rebuild (soft
// signals); status is authoritative from provider_keys.
func (e *Engine) refreshPool(sel string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, found, err := e.store.LoadPoolBySelector(ctx, sel)
	if err != nil || !found {
		return
	}
	built := buildPoolState(data.Selector, data.Strategy, data.Params, data.Rows, e.bandit)
	e.mu.Lock()
	e.pools[sel] = built
	e.mu.Unlock()
}

// Reconcile rewrites key_budgets.day_used from the usage_events ground truth (nightly job; no-op
// until usage_events lands in P4). Returns the number of keys reconciled.
func (e *Engine) Reconcile(ctx context.Context) (int, error) {
	return reconcile(ctx, e.store)
}

// --- region context ---

type regionCtxKey struct{}

// WithRegion tags ctx with the request region for region_based selection.
func WithRegion(ctx context.Context, region string) context.Context {
	return context.WithValue(ctx, regionCtxKey{}, region)
}

func regionFromContext(ctx context.Context) string {
	r, _ := ctx.Value(regionCtxKey{}).(string)
	return r
}
