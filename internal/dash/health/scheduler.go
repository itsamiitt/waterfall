package health

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

const (
	defaultConcurrency = 4
	defaultTick        = time.Second
)

// Scheduler runs jittered, per-Provider scheduled health checks driven by health_schedules. Each
// tick it loads the current check targets and dispatches a bounded number of probes (worker-pool
// semaphore, <= Concurrency in flight); each probe is a bounded CheckFunc whose result is written
// as one provider_health_checks row. The clock, the check function, and the jitter source are all
// injectable so tests are deterministic and never hit the network.
type Scheduler struct {
	store Store
	check CheckFunc
	now   func() time.Time
	log   *slog.Logger
	tick  time.Duration
	sem   chan struct{}

	rmu  sync.Mutex
	rand *rand.Rand

	// nextDue is owned by the loop goroutine (and by direct runOnce callers in tests); it is not
	// shared with the per-probe worker goroutines, so it needs no lock.
	nextDue map[string]time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewScheduler builds a Scheduler from Deps. Concurrency defaults to 4, tick to 1s. When Deps.Check
// is nil a production probe (NewProbeCheck over Deps.Resolver) is used.
func NewScheduler(d Deps) *Scheduler {
	conc := d.Concurrency
	if conc < 1 {
		conc = defaultConcurrency
	}
	tick := d.Tick
	if tick <= 0 {
		tick = defaultTick
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	check := d.Check
	if check == nil {
		check = NewProbeCheck(d.Resolver, now)
	}
	return &Scheduler{
		store:   d.storeOrPG(),
		check:   check,
		now:     now,
		log:     log,
		tick:    tick,
		sem:     make(chan struct{}, conc),
		rand:    rand.New(rand.NewSource(now().UnixNano())),
		nextDue: map[string]time.Time{},
	}
}

// Start launches the background loop. Idempotent-safe to call once; Stop cancels it.
func (s *Scheduler) Start() {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.wg.Add(1)
	go s.loop()
}

// Stop cancels the loop and any in-flight probes and waits for them to drain.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *Scheduler) loop() {
	defer s.wg.Done()
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			targets, err := s.store.ListCheckTargets(s.ctx)
			if err != nil {
				s.log.Warn("health scheduler: list targets", "err", err)
				continue
			}
			s.runOnce(s.ctx, targets)
		}
	}
}

// runOnce dispatches a probe for every target whose next-due time has arrived, bounded by the
// worker-pool semaphore (<= cap(sem) probes concurrently). It waits for the dispatched probes to
// finish before returning so a caller (and Stop) never leaks goroutines. A target's next-due time
// is advanced by a jittered interval so probes de-synchronize across Providers.
func (s *Scheduler) runOnce(ctx context.Context, targets []Target) {
	now := s.now()
	var wg sync.WaitGroup
	for _, t := range targets {
		if !t.Enabled || ctx.Err() != nil {
			continue
		}
		if due, ok := s.nextDue[t.ProviderID]; ok && now.Before(due) {
			continue
		}
		s.nextDue[t.ProviderID] = now.Add(s.jittered(t))

		wg.Add(1)
		tt := t
		go func() {
			defer wg.Done()
			select {
			case s.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-s.sem }()

			res := s.check(ctx, tt)
			if res.Region == "" {
				res.Region = firstRegion(tt.Regions)
			}
			if err := s.store.WriteCheck(ctx, tt.ProviderID, res, s.now()); err != nil {
				s.log.Warn("health scheduler: write check", "provider", tt.ProviderID, "err", err)
			}
		}()
	}
	wg.Wait()
}

// jittered returns the interval for the target with +/- jitter_pct% uniform noise applied, so
// scheduled probes spread out instead of firing in lockstep. Bounded to [interval*(1-p), interval*
// (1+p)] and never below 1s.
func (s *Scheduler) jittered(t Target) time.Duration {
	iv := t.IntervalS
	if iv < 1 {
		iv = 60
	}
	base := time.Duration(iv) * time.Second
	p := t.JitterPct
	if p <= 0 {
		return base
	}
	if p > 100 {
		p = 100
	}
	s.rmu.Lock()
	frac := (s.rand.Float64()*2 - 1) * (float64(p) / 100) // in [-p/100, +p/100]
	s.rmu.Unlock()
	d := time.Duration(float64(base) * (1 + frac))
	if d < time.Second {
		d = time.Second
	}
	return d
}
