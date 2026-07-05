package workers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/metrics"
)

// Detector is the server-derived worker-lost loop (doc 06 §4). A crashed worker cannot report
// its own death, so the detector marks status='lost' when last_heartbeat_at is older than the
// staleness threshold (default 3 heartbeat intervals). To avoid flapping on GC pauses / network
// jitter, the transition (and its alert) require the overdue condition on TWO consecutive passes
// (hysteresis) — a worker that recovers within one pass never trips the alert (F7, doc 06 §4).
//
// The clock is injectable so the P5 acceptance test drives passes deterministically. Concurrency:
// the strike map is mutex-guarded; the loop calls Pass on a ticker and Stop joins it cleanly.
type Detector struct {
	store      LostStore
	now        func() time.Time
	interval   time.Duration
	threshold  time.Duration // staleness cutoff = missIntervals * interval
	hysteresis int
	onLost     func(id string)
	log        *slog.Logger
	lost       *metrics.Counter

	mu      sync.Mutex
	strikes map[string]int
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// DetectorConfig wires a Detector. Zero values fall back to production defaults (10s interval,
// 3 missed intervals, 2-pass hysteresis).
type DetectorConfig struct {
	Store         LostStore
	Now           func() time.Time
	Interval      time.Duration
	MissIntervals int // staleness threshold in heartbeat intervals (default 3)
	Hysteresis    int // consecutive overdue passes before lost fires (default 2)
	OnLost        func(id string)
	Logger        *slog.Logger
	Metrics       *metrics.Registry
}

// NewDetector builds a Detector from cfg.
func NewDetector(cfg DetectorConfig) *Detector {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.MissIntervals <= 0 {
		cfg.MissIntervals = 3
	}
	if cfg.Hysteresis <= 0 {
		cfg.Hysteresis = 2
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	reg := cfg.Metrics
	if reg == nil {
		reg = metrics.New()
	}
	return &Detector{
		store:      cfg.Store,
		now:        cfg.Now,
		interval:   cfg.Interval,
		threshold:  time.Duration(cfg.MissIntervals) * cfg.Interval,
		hysteresis: cfg.Hysteresis,
		onLost:     cfg.OnLost,
		log:        cfg.Logger,
		lost:       reg.Counter("dash_workers_lost_total", "workers transitioned to lost by the detector"),
		strikes:    map[string]int{},
	}
}

// Pass runs one detector sweep at wall time now: it strikes every overdue worker and, once a
// worker's consecutive strike count reaches the hysteresis, commits status='lost' and fires the
// alert exactly once. Workers no longer overdue have their strike count reset (flap suppression).
// Returns the ids newly marked lost this pass (for tests).
func (d *Detector) Pass(ctx context.Context, now time.Time) ([]string, error) {
	cutoff := now.Add(-d.threshold)
	overdue, err := d.store.OverdueWorkers(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	overdueSet := make(map[string]bool, len(overdue))
	for _, o := range overdue {
		overdueSet[o.ID] = true
	}

	d.mu.Lock()
	// Reset strikes for any worker that is no longer overdue (recovered / re-adopted).
	for id := range d.strikes {
		if !overdueSet[id] {
			delete(d.strikes, id)
		}
	}
	var toMark []string
	for _, o := range overdue {
		d.strikes[o.ID]++
		if d.strikes[o.ID] == d.hysteresis {
			toMark = append(toMark, o.ID)
		}
	}
	d.mu.Unlock()

	var newlyLost []string
	for _, id := range toMark {
		changed, merr := d.store.MarkLost(ctx, id)
		if merr != nil {
			d.log.Error("mark lost failed", "worker", id, "err", merr)
			continue
		}
		if changed {
			newlyLost = append(newlyLost, id)
			d.lost.Inc()
			if d.onLost != nil {
				d.onLost(id)
			}
			d.log.Warn("worker lost", "worker", id)
		}
	}
	return newlyLost, nil
}

// Start launches the detector loop on the heartbeat interval until Stop. Safe to call once.
func (d *Detector) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		t := time.NewTicker(d.interval)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if _, err := d.Pass(runCtx, d.now()); err != nil {
					d.log.Error("detector pass failed", "err", err)
				}
			}
		}
	}()
}

// Stop cancels the loop and waits for the goroutine to exit.
func (d *Detector) Stop() {
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
}
