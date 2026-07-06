package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// Poller is the per-instance read-poller (ADR-0019): the GUARANTEED Source implementation that
// derives the closed event vocabulary from the database each tick, so fan-out works with zero
// push infrastructure. DB read rate is O(instances), never O(clients). An optional NOTIFY
// waker (Poke) shortens latency; the poll loop remains the correctness contract.
//
// Per-topic sources (all reads bounded; all under ONE PlatformTx per tick; v1 is
// operator-scoped platform telemetry per doc 12 §P7 — payloads are aggregates or entity id +
// closed-vocabulary state, never row payloads/PII):
//
//	overview  <- self_monitor key='overview_snapshot' (seq bump => overview.tiles.tick with the
//	             leader aggregator's persisted tile snapshot — doc 02 §4.3 exactly)
//	queue     <- self_monitor key='queue_stats_sample' (seq bump => queue.stats.tick with the
//	             sampler's persisted per-queue state vector; closes OI-QW-9)
//	provider  <- providers.updated_at watermark (catalog/op-state/health mutations, incl. every
//	             config_epochs('platform','provider_catalog') bump path, which always updates
//	             the row) => provider.health.changed {provider_id} {status, op_state}
//	key       <- provider_keys.updated_at watermark (KM-3 transitions bump updated_at)
//	             => key.status.changed {key_id, provider_id} {status}
//	worker    <- workers (id, status, desired_state) diffed against the previous tick
//	             => worker.state.changed {worker_id} {status, desired_state}
//	alert     <- alert_events id watermark (fired) + resolved_at watermark (resolved), readable
//	             cross-tenant via alert_events_operator_read => alert.event.fired/.resolved
//	             {episode_id, rule_id, tenant_id} {value, state}
//	import    <- bulk_jobs progress counters diffed per in-flight job. bulk_jobs carries
//	             tenant-isolation RLS ONLY, so under PlatformTx the poller sees the platform
//	             Tenant's (operator) jobs — tenant-scoped BYO import streams are deferred with
//	             the v1 operator scoping => import.batch.progress {job_id, kind}
//	             {status, total, succeeded, failed}
//	approval  <- approval_requests (id, status) diffed per pending/recent request (same
//	             platform-Tenant visibility note) => approval.request.changed {request_id}
//	             {status, action_kind}
type Poller struct {
	store *db.Store
	hub   *Hub
	log   *slog.Logger
	cfg   PollerConfig

	poke   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}

	// watermarks + diff state (single goroutine; no locking needed)
	seeded        bool
	overviewSeq   int64
	queueSeq      int64
	providerWM    time.Time
	keyWM         time.Time
	alertMaxID    int64
	alertResolved time.Time
	workers       map[string]string // id -> status|desired
	jobs          map[string]string // id -> status|succeeded|failed|total
	approvals     map[string]string // id -> status
}

// PollerConfig tunes the poll cadence. Zero interval = 1s.
type PollerConfig struct {
	Interval time.Duration
}

// NewPoller builds the poller over the shared store, publishing into hub.
func NewPoller(store *db.Store, hub *Hub, cfg PollerConfig, log *slog.Logger) *Poller {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		store: store, hub: hub, log: log, cfg: cfg,
		poke:      make(chan struct{}, 1),
		workers:   map[string]string{},
		jobs:      map[string]string{},
		approvals: map[string]string{},
	}
}

// Poke requests an immediate poll (NOTIFY wake; safe from any goroutine; coalesced).
func (p *Poller) Poke() {
	select {
	case p.poke <- struct{}{}:
	default:
	}
}

// Start seeds the watermarks (no historical flood on boot) and runs the poll loop.
func (p *Poller) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		t := time.NewTicker(p.cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
			case <-p.poke:
			}
			if err := p.tick(runCtx); err != nil {
				// Log-and-continue: a failed poll is lag, never loss — the next tick
				// re-derives from the same watermarks.
				p.log.Warn("realtime poller tick", "err", err)
			}
		}
	}()
}

// Stop cancels the loop and waits for it to exit.
func (p *Poller) Stop() {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
}

// tick runs one derivation pass. The first pass only seeds watermarks/diff state.
func (p *Poller) tick(ctx context.Context) error {
	var evs []Event
	err := p.store.PlatformTx(ctx, func(c *pg.Conn) error {
		var e []Event
		var err error
		if e, err = p.pollSnapshots(c); err != nil {
			return err
		}
		evs = append(evs, e...)
		if e, err = p.pollProviders(c); err != nil {
			return err
		}
		evs = append(evs, e...)
		if e, err = p.pollKeys(c); err != nil {
			return err
		}
		evs = append(evs, e...)
		if e, err = p.pollWorkers(c); err != nil {
			return err
		}
		evs = append(evs, e...)
		if e, err = p.pollAlerts(c); err != nil {
			return err
		}
		evs = append(evs, e...)
		if e, err = p.pollImports(c); err != nil {
			return err
		}
		evs = append(evs, e...)
		if e, err = p.pollApprovals(c); err != nil {
			return err
		}
		evs = append(evs, e...)
		return nil
	})
	if err != nil {
		return err
	}
	first := !p.seeded
	p.seeded = true
	if first {
		return nil // seed pass: watermarks set, nothing published
	}
	for _, e := range evs {
		p.hub.Publish(e)
	}
	return nil
}

// pollSnapshots derives overview.tiles.tick + queue.stats.tick from the self_monitor rows.
func (p *Poller) pollSnapshots(c *pg.Conn) ([]Event, error) {
	res, err := c.Query(`select key, seq, payload from self_monitor
		where key in ('overview_snapshot', 'queue_stats_sample')`)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, r := range res.Rows {
		key, seq := str(r[0]), i64(r[1])
		payload := json.RawMessage(str(r[2]))
		switch key {
		case "overview_snapshot":
			if seq > p.overviewSeq {
				p.overviewSeq = seq
				out = append(out, Event{Name: "overview.tiles.tick", Payload: payload})
			}
		case "queue_stats_sample":
			if seq > p.queueSeq {
				p.queueSeq = seq
				out = append(out, Event{Name: "queue.stats.tick", Payload: payload})
			}
		}
	}
	return out, nil
}

// pollProviders emits provider.health.changed for rows whose updated_at passed the watermark.
func (p *Poller) pollProviders(c *pg.Conn) ([]Event, error) {
	if p.providerWM.IsZero() {
		p.providerWM = time.Now().UTC()
	}
	res, err := c.QueryParams(`select id, status, op_state, updated_at from providers
		where updated_at > $1 order by updated_at asc limit 200`, p.providerWM)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, r := range res.Rows {
		if ts := parseTS(str(r[3])); ts.After(p.providerWM) {
			p.providerWM = ts
		}
		out = append(out, Event{
			Name:    "provider.health.changed",
			Scope:   map[string]string{"provider_id": str(r[0])},
			Payload: map[string]string{"status": str(r[1]), "op_state": str(r[2])},
		})
	}
	return out, nil
}

// pollKeys emits key.status.changed for provider_keys rows past the watermark (KM-3
// transitions always bump updated_at).
func (p *Poller) pollKeys(c *pg.Conn) ([]Event, error) {
	if p.keyWM.IsZero() {
		p.keyWM = time.Now().UTC()
	}
	res, err := c.QueryParams(`select id, provider_id, status, updated_at from provider_keys
		where updated_at > $1 order by updated_at asc limit 500`, p.keyWM)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, r := range res.Rows {
		if ts := parseTS(str(r[3])); ts.After(p.keyWM) {
			p.keyWM = ts
		}
		out = append(out, Event{
			Name:    "key.status.changed",
			Scope:   map[string]string{"key_id": str(r[0]), "provider_id": str(r[1])},
			Payload: map[string]string{"status": str(r[2])},
		})
	}
	return out, nil
}

// pollWorkers diffs the (bounded) worker registry's status/desired_state vector.
func (p *Poller) pollWorkers(c *pg.Conn) ([]Event, error) {
	res, err := c.Query(`select id, status, desired_state from workers order by id limit 1000`)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(res.Rows))
	var out []Event
	for _, r := range res.Rows {
		id, status, desired := str(r[0]), str(r[1]), str(r[2])
		seen[id] = true
		state := status + "|" + desired
		if p.workers[id] == state {
			continue
		}
		p.workers[id] = state
		out = append(out, Event{
			Name:    "worker.state.changed",
			Scope:   map[string]string{"worker_id": id},
			Payload: map[string]string{"status": status, "desired_state": desired},
		})
	}
	for id := range p.workers {
		if !seen[id] {
			delete(p.workers, id)
		}
	}
	return out, nil
}

// pollAlerts emits alert.event.fired for episodes past the id watermark and
// alert.event.resolved for resolutions past the resolved_at watermark (operator_read policy).
func (p *Poller) pollAlerts(c *pg.Conn) ([]Event, error) {
	var out []Event
	if p.alertMaxID == 0 {
		if res, err := c.Query(`select coalesce(max(id),0) from alert_events`); err != nil {
			return nil, err
		} else if len(res.Rows) > 0 {
			p.alertMaxID = i64(res.Rows[0][0])
		}
		p.alertResolved = time.Now().UTC()
		return nil, nil
	}
	res, err := c.QueryParams(`select id, tenant_id, rule_id, value from alert_events
		where id > $1 order by id asc limit 200`, p.alertMaxID)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		if id := i64(r[0]); id > p.alertMaxID {
			p.alertMaxID = id
		}
		out = append(out, Event{
			Name:    "alert.event.fired",
			Scope:   map[string]string{"episode_id": str(r[0]), "rule_id": str(r[2]), "tenant_id": str(r[1])},
			Payload: map[string]any{"state": "firing", "value": f64(r[3])},
		})
	}
	res, err = c.QueryParams(`select id, tenant_id, rule_id, resolved_at from alert_events
		where resolved_at > $1 order by resolved_at asc limit 200`, p.alertResolved)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		if ts := parseTS(str(r[3])); ts.After(p.alertResolved) {
			p.alertResolved = ts
		}
		out = append(out, Event{
			Name:    "alert.event.resolved",
			Scope:   map[string]string{"episode_id": str(r[0]), "rule_id": str(r[2]), "tenant_id": str(r[1])},
			Payload: map[string]string{"state": "resolved"},
		})
	}
	return out, nil
}

// pollImports diffs bulk_jobs progress counters for in-flight or just-finished jobs.
func (p *Poller) pollImports(c *pg.Conn) ([]Event, error) {
	res, err := c.Query(`select id, kind, status, total, succeeded, failed from bulk_jobs
		where status in ('queued','running')
		   or finished_at > now() - interval '2 minutes'
		order by created_at desc limit 200`)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, r := range res.Rows {
		id, kind, status := str(r[0]), str(r[1]), str(r[2])
		total, ok, fail := str(r[3]), str(r[4]), str(r[5])
		state := status + "|" + ok + "|" + fail + "|" + total
		if p.jobs[id] == state {
			continue
		}
		p.jobs[id] = state
		out = append(out, Event{
			Name:  "import.batch.progress",
			Scope: map[string]string{"job_id": id, "kind": kind},
			Payload: map[string]any{
				"status": status, "total": i64(r[3]), "succeeded": i64(r[4]), "failed": i64(r[5]),
			},
		})
	}
	return out, nil
}

// pollApprovals diffs approval_requests statuses for pending/recent requests.
func (p *Poller) pollApprovals(c *pg.Conn) ([]Event, error) {
	res, err := c.Query(`select id, action_kind, status from approval_requests
		where status = 'pending' or created_at > now() - interval '1 hour'
		order by created_at desc limit 200`)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, r := range res.Rows {
		id, kind, status := str(r[0]), str(r[1]), str(r[2])
		if p.approvals[id] == status {
			continue
		}
		p.approvals[id] = status
		out = append(out, Event{
			Name:    "approval.request.changed",
			Scope:   map[string]string{"request_id": id},
			Payload: map[string]string{"status": status, "action_kind": kind},
		})
	}
	return out, nil
}

// SnapshotQueueStats builds the sampler's queue snapshot from the newest queue_stats_1m bucket
// per queue and persists it as the self_monitor 'queue_stats_sample' row (doc 06 §6, closing
// OI-QW-9): followers and every instance's poller serve queue.stats.tick from this one row.
func SnapshotQueueStats(ctx context.Context, store *db.Store, m *SelfMon) error {
	type qrow struct {
		Queue      string `json:"queue"`
		Depth      int64  `json:"depth"`
		Running    int64  `json:"running"`
		Retry      int64  `json:"retry"`
		Failed     int64  `json:"failed"`
		Dead       int64  `json:"dead"`
		Enq        int64  `json:"enq"`
		Deq        int64  `json:"deq"`
		OldestAgeS int64  `json:"oldest_age_s"`
	}
	var rows []qrow
	err := store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query(`select distinct on (queue)
			  queue, depth, running, retry, failed, dead, enq, deq, oldest_age_s
			from queue_stats_1m order by queue, bucket_start desc limit 200`)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			rows = append(rows, qrow{
				Queue: str(r[0]), Depth: i64(r[1]), Running: i64(r[2]), Retry: i64(r[3]),
				Failed: i64(r[4]), Dead: i64(r[5]), Enq: i64(r[6]), Deq: i64(r[7]),
				OldestAgeS: i64(r[8]),
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	payload, err := marshal(map[string]any{
		"ts":     time.Now().UTC().Format(time.RFC3339),
		"queues": rows,
	})
	if err != nil {
		return err
	}
	_, err = m.UpsertSnapshot(ctx, "queue_stats_sample", "queue_sampler", payload)
	return err
}
