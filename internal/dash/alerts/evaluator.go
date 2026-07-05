package alerts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
)

// resolveHysteresis is the doc-10 §5.1 rule: a firing episode resolves only after this many
// consecutive clean (non-breaching) evaluations — fastest resolve ~90s after recovery.
const resolveHysteresis = 3

// evalAction is the edge-triggered transition a cycle produces for one rule+scope.
type evalAction int

const (
	actNone evalAction = iota
	actFire
	actRenotify
	actResolve
)

// episode is the state the pure decision needs. It is rebuilt each cycle from the open alert_events
// row (open/notifiedAt/firedAt/acked) plus the evaluator's in-memory cleanStreak cache (doc 10
// §5.5: last-state is a cache; the single-firing index makes the rebuild race-free).
type episode struct {
	open        bool
	notifiedAt  time.Time
	firedAt     time.Time
	acked       bool
	cleanStreak int
}

// decideNotify is the pure edge-trigger + cooldown-renotify + resolve-hysteresis state machine
// (doc 10 §5.1/§5.3). It performs no I/O so the P6 gate #1 acceptance test drives it directly with
// an injectable clock: sustained breach fires exactly once, renotifies once per elapsed cooldown
// (unless acked), and resolves once after 3 clean cycles (resolve notifies even when acked).
func decideNotify(ep episode, breaching bool, now time.Time, cooldown time.Duration) (evalAction, episode) {
	if breaching {
		ep.cleanStreak = 0
		if !ep.open {
			ep.open = true
			ep.firedAt = now
			ep.notifiedAt = now
			ep.acked = false // a (re-)fire clears any prior ack
			return actFire, ep
		}
		if !ep.acked && cooldown > 0 && now.Sub(ep.notifiedAt) > cooldown {
			ep.notifiedAt = now
			return actRenotify, ep
		}
		return actNone, ep
	}
	// not breaching
	if ep.open {
		ep.cleanStreak++
		if ep.cleanStreak >= resolveHysteresis {
			ep.open = false
			return actResolve, ep
		}
	}
	return actNone, ep
}

// auditor is the consumer-side slice of audit.Log the evaluator uses for best-effort episode
// evidence. Satisfied by *audit.Log; may be nil.
type auditor interface {
	Append(ctx context.Context, e audit.Entry) error
}

var _ auditor = (*audit.Log)(nil)

// Evaluator runs the 30s alerting cycle over the rollups. Only the leader instance runs it (loops.go
// advisory lock). It holds the cleanStreak cache guarded by a mutex so -race is clean.
type Evaluator struct {
	store *Store
	audit auditor
	now   func() time.Time
	log   *slog.Logger

	evalC *metrics.Counter

	mu    sync.Mutex
	clean map[string]int // rule_id -> consecutive clean evaluations
}

// NewEvaluator builds an Evaluator. now may be nil (wall clock); reg/audit/log may be nil.
func NewEvaluator(store *Store, adt auditor, now func() time.Time, reg *metrics.Registry, log *slog.Logger) *Evaluator {
	if now == nil {
		now = time.Now
	}
	if reg == nil {
		reg = metrics.New()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Evaluator{
		store: store, audit: adt, now: now, log: log,
		evalC: reg.Counter("dash_alert_evaluations_total", "alert rule evaluations", "outcome"),
		clean: map[string]int{},
	}
}

// EvaluateOnce runs one full cycle across all active Tenants (+platform). Errors on a single Tenant
// or rule are logged and skipped so one bad rule never wedges the loop.
func (e *Evaluator) EvaluateOnce(ctx context.Context) error {
	tenants, err := e.store.activeTenantIDs(ctx)
	if err != nil {
		return err
	}
	for _, tid := range tenants {
		if err := e.evaluateTenant(ctx, tid); err != nil {
			e.log.Warn("alert evaluate tenant", "tenant", tid, "err", err)
		}
	}
	return nil
}

// evaluateTenant loads tid's enabled rules and evaluates each.
func (e *Evaluator) evaluateTenant(ctx context.Context, tid string) error {
	var rules []Rule
	err := e.store.tenantTx(ctx, tid, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select id, name, metric, scope, op, threshold, window_s, cooldown_s, severity,
			        channels, enabled, muted_until, created_by, updated_at
			   from alert_rules where enabled = true`)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			rules = append(rules, scanRule(r))
		}
		return nil
	})
	if err != nil {
		return err
	}
	now := e.now().UTC()
	for _, r := range rules {
		if r.MutedUntil != nil && r.MutedUntil.After(now) {
			e.evalC.Inc("suppressed")
			continue
		}
		if e.suppressedByMaintenance(ctx, r) {
			e.suppress(ctx, tid, r, now)
			e.evalC.Inc("suppressed")
			continue
		}
		mr, err := e.readMetric(ctx, tid, r, now)
		if err != nil {
			e.log.Warn("alert metric eval", "rule", r.ID, "metric", r.Metric, "err", err)
			e.evalC.Inc("error")
			continue
		}
		if err := e.applyRule(ctx, tid, r, mr, now); err != nil {
			e.log.Warn("alert apply rule", "rule", r.ID, "err", err)
			e.evalC.Inc("error")
			continue
		}
		e.evalC.Inc("ok")
	}
	return nil
}

// readMetric evaluates the metric value on the correctly-bound connection (platform for Class-P
// sources, the owning Tenant for cost.*).
func (e *Evaluator) readMetric(ctx context.Context, tid string, r Rule, now time.Time) (metricResult, error) {
	var mr metricResult
	fn := func(c *pg.Conn) error {
		v, err := evalMetric(c, r, now)
		mr = v
		return err
	}
	if metricReadsPlatform(r.Metric) {
		return mr, e.store.platformTx(ctx, fn)
	}
	return mr, e.store.tenantTx(ctx, tid, fn)
}

// suppressedByMaintenance reports whether the rule's scoped Provider is in op_state
// maintenance/paused (doc 10 §5.1). Only provider-scoped rules can be suppressed this way.
func (e *Evaluator) suppressedByMaintenance(ctx context.Context, r Rule) bool {
	pid := r.Scope["provider_id"]
	if pid == "" {
		return false
	}
	suppressed := false
	_ = e.store.platformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select op_state from providers where id = $1`, pid)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			st := *res.Rows[0][0]
			suppressed = st == "maintenance" || st == "paused"
		}
		return nil
	})
	return suppressed
}

// suppress auto-resolves any open episode for a maintenance-suppressed rule with a note.
func (e *Evaluator) suppress(ctx context.Context, tid string, r Rule, now time.Time) {
	err := e.store.tenantTx(ctx, tid, func(c *pg.Conn) error {
		return c.ExecParams(
			`update alert_events set state='resolved', resolved_at=$2
			   where rule_id=$1 and state='firing'`, r.ID, now)
	})
	if err != nil {
		e.log.Warn("alert suppress resolve", "rule", r.ID, "err", err)
		return
	}
	e.clearClean(r.ID)
}

// applyRule loads the open episode, runs the pure decision, and applies the transition +
// notification enqueue in ONE tenant transaction (episode row + outbox rows commit together).
func (e *Evaluator) applyRule(ctx context.Context, tid string, r Rule, mr metricResult, now time.Time) error {
	return e.store.tenantTx(ctx, tid, func(c *pg.Conn) error {
		open, err := loadOpenEpisode(c, r.ID)
		if err != nil {
			return err
		}
		ep := episode{cleanStreak: e.getClean(r.ID)}
		if open != nil {
			ep.open = true
			ep.notifiedAt = open.NotifiedAt
			ep.firedAt = open.FiredAt
			ep.acked = open.Acked
		}
		breaching := mr.breaching // empty windows already resolved to breaching=false, hasData=false
		action, next := decideNotify(ep, breaching, now, time.Duration(r.CooldownS)*time.Second)
		e.setClean(r.ID, next.cleanStreak)

		eventDedupe := eventDedupeKey(tid, r.ID, r.Scope)
		switch action {
		case actFire:
			return e.doFire(c, tid, r, mr, now, eventDedupe)
		case actRenotify:
			bucket := int64(0)
			if r.CooldownS > 0 {
				bucket = int64(now.Sub(ep.firedAt) / (time.Duration(r.CooldownS) * time.Second))
			}
			return e.doRenotify(c, r, mr, now, open.ID, eventDedupe, occRenotify(bucket))
		case actResolve:
			e.clearClean(r.ID)
			return e.doResolve(c, tid, r, now, open.ID, eventDedupe)
		default:
			if open != nil && mr.hasData {
				return c.ExecParams(`update alert_events set value=$2 where id=$1`, open.ID, mr.value)
			}
			return nil
		}
	})
}

// doFire inserts the firing episode with ON CONFLICT DO NOTHING against the single-firing partial
// unique index; a notification per channel is enqueued ONLY when this INSERT actually created the
// episode (RETURNING id), so a concurrent/restarted evaluator that loses the race enqueues nothing.
func (e *Evaluator) doFire(c *pg.Conn, tid string, r Rule, mr metricResult, now time.Time, eventDedupe string) error {
	res, err := c.QueryParams(
		`insert into alert_events (tenant_id, rule_id, state, value, fired_at, notified_at, dedupe_key)
		 values (app_current_tenant(), $1, 'firing', $2, $3, $3, $4)
		 on conflict (tenant_id, rule_id) where state='firing' do nothing
		 returning id`,
		r.ID, mr.value, now, eventDedupe)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return nil // lost the fire race; the winner enqueues
	}
	eventID := i64(res.Rows[0][0])
	e.auditEpisode(c, tid, r, "alert_fired")
	return enqueueNotifications(c, r.Channels, eventID, eventDedupe, occFired, now)
}

// doRenotify updates notified_at and enqueues a renotify-occasion notification.
func (e *Evaluator) doRenotify(c *pg.Conn, r Rule, mr metricResult, now time.Time, eventID int64, eventDedupe string, occ occasion) error {
	if err := c.ExecParams(`update alert_events set notified_at=$2, value=$3 where id=$1`, eventID, now, mr.value); err != nil {
		return err
	}
	return enqueueNotifications(c, r.Channels, eventID, eventDedupe, occ, now)
}

// doResolve marks the episode resolved and enqueues the resolved notification (resolve always
// notifies, even after ack).
func (e *Evaluator) doResolve(c *pg.Conn, tid string, r Rule, now time.Time, eventID int64, eventDedupe string) error {
	if err := c.ExecParams(
		`update alert_events set state='resolved', resolved_at=$2 where id=$1`, eventID, now); err != nil {
		return err
	}
	e.auditEpisode(c, tid, r, "alert_resolved")
	return enqueueNotifications(c, r.Channels, eventID, eventDedupe, occResolved, now)
}

// auditEpisode is a best-effort evidence append on the SAME connection (so it commits with the
// transition). tenant_id comes from a Principal ctx matching the bound Tenant. A nil auditor or an
// append error is logged, never fatal.
func (e *Evaluator) auditEpisode(c *pg.Conn, tid string, r Rule, action string) {
	if e.audit == nil {
		return
	}
	ac, ok := e.audit.(interface {
		AppendConn(ctx context.Context, c *pg.Conn, en audit.Entry) error
	})
	if !ok {
		return
	}
	pctx := principalCtx(context.Background(), tid)
	if err := ac.AppendConn(pctx, c, audit.Entry{
		Action: action, ObjectKind: "alert_rule", ObjectID: r.ID,
	}); err != nil {
		e.log.Warn("alert episode audit", "rule", r.ID, "action", action, "err", err)
	}
}

// openEpisode is the minimal open-episode row the decision needs.
type openEpisode struct {
	ID         int64
	NotifiedAt time.Time
	FiredAt    time.Time
	Acked      bool
}

// loadOpenEpisode returns the single open (firing) episode for a rule, or nil.
func loadOpenEpisode(c *pg.Conn, ruleID string) (*openEpisode, error) {
	res, err := c.QueryParams(
		`select id, notified_at, fired_at, ack_at from alert_events
		  where rule_id=$1 and state='firing' order by fired_at desc limit 1`, ruleID)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, nil
	}
	row := res.Rows[0]
	return &openEpisode{
		ID:         i64(row[0]),
		NotifiedAt: parseTS(str(row[1])),
		FiredAt:    parseTS(str(row[2])),
		Acked:      row[3] != nil,
	}, nil
}

// enqueueNotifications inserts one pending outbox row per channel for the occasion, deduped by the
// notification-grained partial unique index (dedupe_key WHERE status='pending') — a re-enqueue of
// the same occasion is a no-op (doc 10 §5.4).
func enqueueNotifications(c *pg.Conn, channels []string, eventID int64, eventDedupe string, occ occasion, now time.Time) error {
	for _, ch := range channels {
		dk := notifDedupeKey(eventDedupe, ch, occ)
		if err := c.ExecParams(
			`insert into alert_notifications
			   (tenant_id, event_id, channel_id, dedupe_key, status, next_retry_at)
			 values (app_current_tenant(), $1, $2, $3, 'pending', $4)
			 on conflict (dedupe_key) where status='pending' do nothing`,
			eventID, ch, dk, now); err != nil {
			return err
		}
	}
	return nil
}

// eventDedupeKey = hex(sha256(tenant || rule || canonical scope-instance)) (doc 03 §2.4).
func eventDedupeKey(tid, ruleID string, scope map[string]string) string {
	h := sha256.New()
	h.Write([]byte(tid))
	h.Write([]byte(ruleID))
	h.Write([]byte(canonicalScope(scope)))
	return hex.EncodeToString(h.Sum(nil))
}

// notifDedupeKey = hex(sha256(event_dedupe_key || ':' || channel_id || ':' || occasion)) (doc 10 §5.4).
func notifDedupeKey(eventDedupe, channelID string, occ occasion) string {
	h := sha256.New()
	h.Write([]byte(eventDedupe))
	h.Write([]byte(":"))
	h.Write([]byte(channelID))
	h.Write([]byte(":"))
	h.Write([]byte(string(occ)))
	return hex.EncodeToString(h.Sum(nil))
}

// --- cleanStreak cache (mutex-guarded for -race) ---

func (e *Evaluator) getClean(ruleID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clean[ruleID]
}

func (e *Evaluator) setClean(ruleID string, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clean[ruleID] = n
}

func (e *Evaluator) clearClean(ruleID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.clean, ruleID)
}
