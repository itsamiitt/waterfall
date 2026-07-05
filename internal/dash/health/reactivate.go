package health

import (
	"context"
	"log/slog"
	"time"
)

const defaultReactivateBatch = 20

// Reactivator runs the auto re-enable probes (brief item 4). Keys sitting in status
// exhausted/rate_limited are candidates to recover; for each, Reactivator asks the injected
// KeyReactivator to Probe it. A successful probe drives the key back toward active THROUGH the
// injected delegate — health owns none of the rotation state machine; it only decides WHICH keys
// to probe and drives the injected interface. This is the single rotation touch-point, and it is
// an interface, so this package never imports internal/dash/rotation.
type Reactivator struct {
	store KeyReactivatorStore
	probe KeyReactivator
	batch int
	log   *slog.Logger
}

// KeyReactivatorStore is the read seam for reactivation candidates (a subset of Store, so *PGStore
// satisfies it).
type KeyReactivatorStore interface {
	ExhaustedKeys(ctx context.Context, limit int) ([]string, error)
}

// NewReactivator builds a Reactivator from Deps. Returns nil when no KeyReactivator is wired (the
// feature is simply inert), so the orchestrator can pass an unset dependency safely.
func NewReactivator(d Deps) *Reactivator {
	if d.Reactivator == nil {
		return nil
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Reactivator{store: d.storeOrPG(), probe: d.Reactivator, batch: defaultReactivateBatch, log: log}
}

// RunOnce probes up to one batch of exhausted/rate_limited keys and returns how many probes
// succeeded and how many were attempted. It is bounded (batch cap) and safe to call on a ticker.
func (r *Reactivator) RunOnce(ctx context.Context) (recovered, attempted int, err error) {
	keys, err := r.store.ExhaustedKeys(ctx, r.batch)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range keys {
		if ctx.Err() != nil {
			break
		}
		attempted++
		if perr := r.probe.Probe(ctx, id); perr != nil {
			r.log.Debug("health reactivator: probe failed", "key", id, "err", perr)
			continue
		}
		recovered++
	}
	return recovered, attempted, nil
}

// Start runs RunOnce on the given interval until ctx is cancelled. Intended to be launched in a
// goroutine by the orchestrator alongside the scheduler.
func (r *Reactivator) Start(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := r.RunOnce(ctx); err != nil {
				r.log.Warn("health reactivator: run", "err", err)
			}
		}
	}
}
