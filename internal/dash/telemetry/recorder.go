package telemetry

import (
	"context"
	"sync"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Recorder performs the single hot-path INSERT into usage_events. usage_events is Class T
// (tenant-isolation RLS, doc 03 §1/§2.6), so the insert binds app.current_tenant to the EVENT's
// Tenant (never 'platform' — a platform-bound tx would fail the WITH CHECK for a customer row).
// The event's TenantID is set by the engine from the Job's already-verified Tenant; Recorder is
// the engine's own telemetry emit, not a request handler, so trusting it is correct here.
type Recorder struct {
	store *db.Store
}

// NewRecorder builds a Recorder over the dashboard Store.
func NewRecorder(store *db.Store) *Recorder { return &Recorder{store: store} }

// Record inserts exactly one usage_events row, bound to ev.TenantID (G1). It returns an error so
// the batched/synchronous callers can react; the fire-and-forget Sink path uses BufferedRecorder.
func (r *Recorder) Record(ctx context.Context, ev UsageEvent) error {
	pctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: ev.TenantID, Scopes: []string{"role:operator"}})
	return r.store.Tx(pctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into usage_events
			   (tenant_id, provider_id, key_id, workflow_key, country, outcome_class, credits, lat_ms)
			 values ($1,$2,$3,$4,$5,$6,$7,$8)`,
			ev.TenantID, ev.ProviderID, nullIfEmpty(ev.KeyID), nullIfEmpty(ev.WorkflowKey),
			nullIfEmpty(ev.Country), ev.OutcomeClass, ev.Credits, ev.LatMs)
	})
}

// RecordBatch inserts a slice of events for ONE Tenant in a single transaction (multi-row
// INSERT) — the BufferedRecorder flush path. All events MUST share tenantID (the caller groups
// by Tenant). An empty batch is a no-op.
func (r *Recorder) RecordBatch(ctx context.Context, tenantID string, evs []UsageEvent) error {
	if len(evs) == 0 {
		return nil
	}
	pctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tenantID, Scopes: []string{"role:operator"}})
	return r.store.Tx(pctx, func(c *pg.Conn) error {
		var sb []byte
		sb = append(sb, `insert into usage_events (tenant_id, provider_id, key_id, workflow_key, country, outcome_class, credits, lat_ms) values `...)
		args := make([]any, 0, len(evs)*8)
		for i, ev := range evs {
			if i > 0 {
				sb = append(sb, ',')
			}
			b := i * 8
			sb = append(sb, placeholders8(b)...)
			args = append(args, ev.TenantID, ev.ProviderID, nullIfEmpty(ev.KeyID),
				nullIfEmpty(ev.WorkflowKey), nullIfEmpty(ev.Country), ev.OutcomeClass, ev.Credits, ev.LatMs)
		}
		return c.ExecParams(string(sb), args...)
	})
}

// BufferedRecorder is the hot-path Sink: Record enqueues onto a bounded channel and returns
// immediately, so a Provider call never blocks on the database. A background flusher drains the
// channel in Tenant-grouped batches. On overflow (channel full) the event is dropped and the
// drop counter is incremented — bounded, observable back-pressure that never wedges enrichment.
type BufferedRecorder struct {
	rec       *Recorder
	ch        chan UsageEvent
	batchMax  int
	dropped   *metrics.Counter
	enqueued  *metrics.Counter
	flushed   *metrics.Counter
	failed    *metrics.Counter
	wg        sync.WaitGroup
	stop      chan struct{}
	stopOnce  sync.Once
	dropCount atomicInt64 // mirror of dropped for test assertions without scraping
}

// BufferedConfig tunes the buffer. Capacity is the channel depth (overflow drops); BatchMax
// caps rows per flush INSERT. Zero values fall back to sane defaults.
type BufferedConfig struct {
	Capacity int
	BatchMax int
}

// NewBufferedRecorder wraps a Recorder with a bounded queue + batching flusher. reg may be nil
// (a private registry is used). Call Start to launch the flusher and Stop for a clean drain.
func NewBufferedRecorder(rec *Recorder, cfg BufferedConfig, reg *metrics.Registry) *BufferedRecorder {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 4096
	}
	if cfg.BatchMax <= 0 {
		cfg.BatchMax = 256
	}
	if reg == nil {
		reg = metrics.New()
	}
	return &BufferedRecorder{
		rec:      rec,
		ch:       make(chan UsageEvent, cfg.Capacity),
		batchMax: cfg.BatchMax,
		dropped:  reg.Counter("dash_usage_events_dropped_total", "usage_events dropped by the buffered recorder on overflow"),
		enqueued: reg.Counter("dash_usage_events_enqueued_total", "usage_events accepted into the buffered recorder queue"),
		flushed:  reg.Counter("dash_usage_events_flushed_total", "usage_events successfully inserted by the buffered recorder"),
		failed:   reg.Counter("dash_usage_events_flush_failed_total", "buffered recorder flush transactions that errored"),
		stop:     make(chan struct{}),
	}
}

// Record enqueues ev without blocking. A full queue drops the event (metric + counter). It
// satisfies Sink.
func (b *BufferedRecorder) Record(_ context.Context, ev UsageEvent) {
	select {
	case b.ch <- ev:
		b.enqueued.Inc()
	default:
		b.dropped.Inc()
		b.dropCount.add(1)
	}
}

// Dropped returns the number of events dropped on overflow (for tests/self-monitoring).
func (b *BufferedRecorder) Dropped() int64 { return b.dropCount.load() }

// Start launches the flusher goroutine. ctx cancellation (or Stop) triggers a final drain.
func (b *BufferedRecorder) Start(ctx context.Context) {
	b.wg.Add(1)
	go b.run(ctx)
}

// Stop signals shutdown and waits for the flusher to drain and exit.
func (b *BufferedRecorder) Stop() {
	b.stopOnce.Do(func() { close(b.stop) })
	b.wg.Wait()
}

func (b *BufferedRecorder) run(ctx context.Context) {
	defer b.wg.Done()
	for {
		select {
		case <-ctx.Done():
			b.drain(context.Background())
			return
		case <-b.stop:
			b.drain(context.Background())
			return
		case ev := <-b.ch:
			b.flush(ctx, ev)
		}
	}
}

// flush collects the triggering event plus whatever else is queued (up to batchMax), groups by
// Tenant, and inserts each group in one transaction.
func (b *BufferedRecorder) flush(ctx context.Context, first UsageEvent) {
	batch := make([]UsageEvent, 0, b.batchMax)
	batch = append(batch, first)
	for len(batch) < b.batchMax {
		select {
		case ev := <-b.ch:
			batch = append(batch, ev)
		default:
			b.insertBatch(ctx, batch)
			return
		}
	}
	b.insertBatch(ctx, batch)
}

// drain flushes everything still queued at shutdown (best-effort, bounded by current depth).
func (b *BufferedRecorder) drain(ctx context.Context) {
	for {
		select {
		case ev := <-b.ch:
			b.flush(ctx, ev)
		default:
			return
		}
	}
}

func (b *BufferedRecorder) insertBatch(ctx context.Context, batch []UsageEvent) {
	byTenant := map[string][]UsageEvent{}
	for _, ev := range batch {
		byTenant[ev.TenantID] = append(byTenant[ev.TenantID], ev)
	}
	for tid, evs := range byTenant {
		if err := b.rec.RecordBatch(ctx, tid, evs); err != nil {
			b.failed.Inc()
			continue
		}
		b.flushed.Add(float64(len(evs)))
	}
}

// NopRecorder is a Sink that discards every event — for tests and for deployments where the
// hot-path feed is disabled. It satisfies Sink.
type NopRecorder struct{}

// Record does nothing.
func (NopRecorder) Record(context.Context, UsageEvent) {}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// placeholders8 renders "($b+1,$b+2,...,$b+8)" for a multi-row INSERT VALUES tuple.
func placeholders8(base int) string {
	var sb []byte
	sb = append(sb, '(')
	for j := 1; j <= 8; j++ {
		if j > 1 {
			sb = append(sb, ',')
		}
		sb = append(sb, '$')
		sb = appendInt(sb, base+j)
	}
	sb = append(sb, ')')
	return string(sb)
}
