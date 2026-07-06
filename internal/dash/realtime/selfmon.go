package realtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
)

// SelfMon is the ONE writer of the self_monitor snapshot row-set (doc 03 §2.7/§6, migration
// 0010): single-row-per-key upserts under PlatformTx (Class P, platform-only RLS). Emitters —
// the overview 2s aggregator, the queue-stats sampler, the telemetry fold-watermark heartbeat,
// and the per-instance SSE client count — all write through this store, keeping the one-owner
// registry honest. Readers: overview followers, the realtime poller, and the P6 `system.*`
// alert-metric branches (system.sse_clients / system.aggregator_lag_s, closing OI-P6-2).
type SelfMon struct {
	store *db.Store
}

// NewSelfMon wraps the shared dual-GUC store.
func NewSelfMon(store *db.Store) *SelfMon { return &SelfMon{store: store} }

// UpsertSnapshot writes a payload snapshot row (overview_snapshot / queue_stats_sample) with a
// DB-side monotonic seq increment, so followers and pollers can detect "new tick" without any
// cross-instance coordination. Returns the new seq.
func (m *SelfMon) UpsertSnapshot(ctx context.Context, key, component string, payload []byte) (int64, error) {
	var seq int64
	err := m.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`insert into self_monitor (key, component, payload, seq)
			values ($1, $2, $3::jsonb, 1)
			on conflict (key) do update set payload = excluded.payload,
			  seq = self_monitor.seq + 1, updated_at = now()
			returning seq`, key, component, string(payload))
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) > 0 {
			seq = i64(res.Rows[0][0])
		}
		return nil
	})
	return seq, err
}

// Snapshot reads one snapshot row. found=false when the key has never been written.
func (m *SelfMon) Snapshot(ctx context.Context, key string) (payload []byte, seq int64, updatedAt time.Time, found bool, err error) {
	err = m.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select payload, seq, updated_at from self_monitor where key = $1`, key)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return nil
		}
		found = true
		if res.Rows[0][0] != nil {
			payload = []byte(*res.Rows[0][0])
		}
		seq = i64(res.Rows[0][1])
		updatedAt = parseTS(str(res.Rows[0][2]))
		return nil
	})
	return payload, seq, updatedAt, found, err
}

// UpsertSSEClients writes this instance's SSE client count (key sse:<instance>), backing the
// doc 10 §4 system.sse_clients metric: SUM(sse_clients) across instances.
func (m *SelfMon) UpsertSSEClients(ctx context.Context, instance string, clients int64) error {
	return m.store.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into self_monitor (key, component, instance, sse_clients)
			values ($1, 'sse', $2, $3)
			on conflict (key) do update set sse_clients = excluded.sse_clients,
			  seq = self_monitor.seq + 1, updated_at = now()`,
			"sse:"+instance, instance, clients)
	})
}

// UpsertWatermark writes a fold family's watermark (key fold:<family>), backing the doc 10 §4
// system.aggregator_lag_s metric: max(now() - watermark_ts) == now() - min(watermark_ts).
func (m *SelfMon) UpsertWatermark(ctx context.Context, family string, watermark time.Time) error {
	return m.store.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into self_monitor (key, component, watermark_ts)
			values ($1, 'aggregator', $2)
			on conflict (key) do update set watermark_ts = excluded.watermark_ts,
			  seq = self_monitor.seq + 1, updated_at = now()`,
			"fold:"+family, watermark.UTC())
	})
}

// HeartbeatAges returns now-updated_at seconds per key for the given key prefixes — the
// overview system_health tile's input (aggregator/evaluator heartbeat freshness, doc 09 §1.2).
func (m *SelfMon) HeartbeatAges(ctx context.Context, keys []string) (map[string]float64, error) {
	out := make(map[string]float64, len(keys))
	err := m.store.PlatformTx(ctx, func(c *pg.Conn) error {
		for _, k := range keys {
			res, qerr := c.QueryParams(
				`select extract(epoch from (now() - updated_at)) from self_monitor where key = $1`, k)
			if qerr != nil {
				return qerr
			}
			if len(res.Rows) > 0 && res.Rows[0][0] != nil {
				out[k] = f64(res.Rows[0][0])
			}
		}
		return nil
	})
	return out, err
}

// InstanceID generates the per-process instance identity used in self_monitor keys and
// bulk-job claims: 8 random hex bytes (no hostname/PII in rows).
func InstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "inst-" + hex.EncodeToString([]byte(time.Now().UTC().Format("150405")))
	}
	return hex.EncodeToString(b[:])
}

// MonitorConfig tunes the self-monitor publisher loop.
type MonitorConfig struct {
	Interval  time.Duration // upsert cadence (default 15s — one heartbeat period)
	Instance  string        // this instance's id (default: random InstanceID)
	Watermark func() time.Time
}

// StartMonitor runs the per-instance self-monitor publisher: every interval it upserts this
// instance's SSE client count and — when a Watermark source is wired (the telemetry
// aggregator's fold watermark; leader-only advancement) — the fold:usage watermark row. This
// closes OI-P6-2: the P6 system.sse_clients / system.aggregator_lag_s evaluator branches now
// read live rows. It also feeds the dash_sse_heartbeat_unixtime dead-man gauge. Returns a stop
// func (idempotent).
func StartMonitor(ctx context.Context, m *SelfMon, s *Streams, cfg MonitorConfig, reg *metrics.Registry, log *slog.Logger) func() {
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}
	if cfg.Instance == "" {
		cfg.Instance = InstanceID()
	}
	if reg == nil {
		reg = metrics.New()
	}
	if log == nil {
		log = slog.Default()
	}
	hbGauge := reg.Gauge("dash_sse_heartbeat_unixtime",
		"unix time of this instance's last successful self_monitor heartbeat (dead-man's switch)")

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				var clients int64
				if s != nil {
					clients = s.ActiveConns()
				}
				if err := m.UpsertSSEClients(runCtx, cfg.Instance, clients); err != nil {
					log.Warn("self_monitor sse heartbeat", "err", err)
					continue
				}
				if cfg.Watermark != nil {
					if wm := cfg.Watermark(); !wm.IsZero() {
						if err := m.UpsertWatermark(runCtx, "usage", wm); err != nil {
							log.Warn("self_monitor fold watermark", "err", err)
							continue
						}
					}
				}
				hbGauge.Set(float64(time.Now().Unix()))
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// --- nullable text column helpers (local copies; no cross-feature import) ---

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	var n int64
	neg := false
	for i, ch := range *p {
		if ch == '-' && i == 0 {
			neg = true
			continue
		}
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int64(ch-'0')
	}
	if neg {
		return -n
	}
	return n
}

func f64(p *string) float64 {
	if p == nil {
		return 0
	}
	v, err := strconv.ParseFloat(*p, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseTS parses a Postgres timestamptz text rendering into a UTC time.
func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999-07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// marshal is a tiny indirection so snapshot writers share one JSON path.
func marshal(v any) ([]byte, error) { return json.Marshal(v) }
