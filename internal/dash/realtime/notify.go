package realtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// NotifyChannel is the LISTEN channel the poller wakes on (ADR-0019 timeboxed extension):
// anything that wants sub-poll-interval latency may `select pg_notify('dash_config', ”)`
// after a config write. The poller remains fully correct without any notifier — NOTIFY only
// shortens the wake latency, never carries data.
const NotifyChannel = "dash_config"

// StartNotifyWaker runs a best-effort LISTEN loop on a DEDICATED non-pooled connection: each
// notification pokes the poller into an immediate poll. Connection failures back off and
// redial; a permanently unavailable LISTEN path degrades to the poll interval (the contract).
// Returns a stop func.
func StartNotifyWaker(ctx context.Context, cfg pg.Config, poller *Poller, log *slog.Logger) func() {
	if log == nil {
		log = slog.Default()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		backoff := time.Second
		for runCtx.Err() == nil {
			c, err := pg.Connect(cfg)
			if err != nil {
				log.Warn("notify waker connect", "err", err)
				sleepCtx(runCtx, backoff)
				backoff = nextBackoff(backoff)
				continue
			}
			if err := c.Listen(NotifyChannel); err != nil {
				log.Warn("notify waker listen", "err", err)
				_ = c.Close()
				sleepCtx(runCtx, backoff)
				backoff = nextBackoff(backoff)
				continue
			}
			backoff = time.Second
			for runCtx.Err() == nil {
				if _, err := c.WaitNotification(runCtx); err != nil {
					if runCtx.Err() == nil {
						log.Warn("notify waker wait", "err", err)
					}
					break
				}
				poller.Poke()
			}
			_ = c.Close()
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}
