package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
)

// ErrEgressBlocked is returned by TestSend when the notifier's SSRF guard refuses the channel
// target (private-range / metadata). The HTTP layer maps it to 403 egress_blocked (doc 12 §P6).
var ErrEgressBlocked = errors.New("alerts: egress blocked by SSRF policy")

// notifier backoff schedule.
const (
	notifBaseBackoff = 30 * time.Second
	notifMaxAttempts = 8
)

// Secrets is the consumer-side slice of the envelope backend the alerts package needs: seal a
// channel config at create, open it for delivery. Satisfied by *secrets.PGBackend / MemoryBackend.
type Secrets interface {
	Seal(ctx context.Context, kind string, plaintext []byte) (secrets.EnvelopeID, error)
	Open(ctx context.Context, id secrets.EnvelopeID) ([]byte, error)
}

var _ Secrets = (secrets.Backend)(nil)

// Notifier delivers the alert_notifications outbox. Only the leader instance runs the loop
// (loops.go advisory lock); the same delivery path is reused by the channel test-send.
type Notifier struct {
	store   *Store
	secrets Secrets
	egress  egressFactory
	now     func() time.Time
	log     *slog.Logger

	deliveredC *metrics.Counter
	pendingG   *metrics.Gauge
}

// NewNotifier builds a Notifier. egress may be nil (defaults to the SSRF-guarded provider egress
// client); now/reg/log may be nil.
func NewNotifier(store *Store, sec Secrets, egress egressFactory, now func() time.Time, reg *metrics.Registry, log *slog.Logger) *Notifier {
	if now == nil {
		now = time.Now
	}
	if egress == nil {
		egress = defaultEgress
	}
	if reg == nil {
		reg = metrics.New()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{
		store: store, secrets: sec, egress: egress, now: now, log: log,
		deliveredC: reg.Counter("dash_alert_notifications_total", "alert notification delivery outcomes", "result"),
		pendingG:   reg.Gauge("dash_alert_notifier_pending", "pending alert notifications in the outbox"),
	}
}

// DeliverOnce drains due notifications across all active Tenants. It claims one row at a time under
// FOR UPDATE SKIP LOCKED, delivers, and marks sent/failed with exponential backoff.
func (n *Notifier) DeliverOnce(ctx context.Context) error {
	tenants, err := n.store.activeTenantIDs(ctx)
	if err != nil {
		return err
	}
	var pending int64
	for _, tid := range tenants {
		for {
			claimed, err := n.deliverNext(ctx, tid)
			if err != nil {
				n.log.Warn("alert deliver", "tenant", tid, "err", err)
				break
			}
			if !claimed {
				break
			}
		}
		pending += n.countPending(ctx, tid)
	}
	n.pendingG.Set(float64(pending))
	return nil
}

// deliverNext claims and delivers a single due notification for tid. It returns claimed=false when
// the outbox has no due rows for tid.
func (n *Notifier) deliverNext(ctx context.Context, tid string) (bool, error) {
	// Phase 1: claim + read the joined delivery context in a short tenant tx.
	var d claim
	found := false
	err := n.store.tenantTx(ctx, tid, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select n.id, n.attempts, e.state, e.value, e.fired_at,
			        r.name, r.metric, r.severity, r.scope, c.kind, c.config_envelope_id, n.dedupe_key
			   from alert_notifications n
			   join alert_events e on e.id = n.event_id
			   join alert_rules r on r.id = e.rule_id
			   join alert_channels c on c.id = n.channel_id
			  where n.status = 'pending' and (n.next_retry_at is null or n.next_retry_at <= now())
			  order by n.next_retry_at nulls first
			  for update of n skip locked
			  limit 1`)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return nil
		}
		row := res.Rows[0]
		d = claim{
			id:         i64(row[0]),
			attempts:   int(i64(row[1])),
			state:      str(row[2]),
			value:      f64(row[3]),
			firedAt:    parseTS(str(row[4])),
			ruleName:   str(row[5]),
			metric:     str(row[6]),
			severity:   str(row[7]),
			scope:      parseScope(str(row[8])),
			kind:       str(row[9]),
			envelopeID: str(row[10]),
			dedupeKey:  str(row[11]),
		}
		found = true
		// Deliver + mark inside the SAME tx so the row stays locked across the attempt (single
		// leader notifier; the lock also prevents a same-cycle re-claim).
		return n.deliverAndMark(ctx, c, d)
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// deliverAndMark opens the sealed channel config, delivers through the identical builder path, and
// marks the outbox row sent or failed-with-backoff, on the already-open tenant connection c.
func (n *Notifier) deliverAndMark(ctx context.Context, c *pg.Conn, d claim) error {
	cfg, cerr := n.openConfig(ctx, d.envelopeID)
	res := deliveryResult{note: "config unavailable"}
	if cerr == nil {
		res = deliver(ctx, n.egress, d.kind, cfg, d.payload())
	}
	switch {
	case res.ok:
		n.deliveredC.Inc("sent")
		return c.ExecParams(
			`update alert_notifications set status='sent', sent_at=$2, attempts=attempts+1 where id=$1`,
			d.id, n.now().UTC())
	default:
		result := "failed"
		if res.blocked {
			result = "ssrf_blocked"
		}
		n.deliveredC.Inc(result)
		attempts := d.attempts + 1
		if attempts >= notifMaxAttempts {
			return c.ExecParams(
				`update alert_notifications set status='failed', attempts=$2 where id=$1`, d.id, attempts)
		}
		backoff := notifBaseBackoff * (1 << uint(minInt(d.attempts, 6)))
		return c.ExecParams(
			`update alert_notifications set attempts=$2, next_retry_at=$3 where id=$1`,
			d.id, attempts, n.now().UTC().Add(backoff))
	}
}

// openConfig opens and unmarshals a sealed channel config.
func (n *Notifier) openConfig(ctx context.Context, envelopeID string) (ChannelConfig, error) {
	pt, err := n.secrets.Open(ctx, secrets.EnvelopeID(envelopeID))
	if err != nil {
		return ChannelConfig{}, err
	}
	var cfg ChannelConfig
	if err := json.Unmarshal(pt, &cfg); err != nil {
		return ChannelConfig{}, err
	}
	return cfg, nil
}

// TestSend delivers a synthetic test notification through the IDENTICAL builder + SSRF-guard path
// (never a shortcut, doc 10 §5.4). It loads the channel for the ctx Principal's Tenant, opens its
// config, and returns the delivery status. A blocked egress surfaces as ErrEgressBlocked.
func (n *Notifier) TestSend(ctx context.Context, channelID string) (TestResult, error) {
	ch, err := n.store.getChannel(ctx, channelID)
	if err != nil {
		return TestResult{}, err
	}
	cfg, err := n.openConfig(ctx, ch.ConfigEnvelopeID)
	if err != nil {
		return TestResult{}, err
	}
	p := notifyPayload{
		Occasion: "test", RuleName: "channel test-send", Metric: "test",
		State: "test", DedupeKey: "test:" + channelID, FiredAt: n.now().UTC(),
	}
	res := deliver(ctx, n.egress, ch.Kind, cfg, p)
	if res.blocked {
		n.deliveredC.Inc("ssrf_blocked")
		return TestResult{}, ErrEgressBlocked
	}
	delivered := res.ok
	if res.ok {
		n.deliveredC.Inc("sent")
	} else {
		n.deliveredC.Inc("failed")
	}
	return TestResult{Delivered: &delivered, StatusCode: res.statusCode, ResponseNote: res.note}, nil
}

// countPending returns the number of pending outbox rows for tid (for the gauge).
func (n *Notifier) countPending(ctx context.Context, tid string) int64 {
	var cnt int64
	_ = n.store.tenantTx(ctx, tid, func(c *pg.Conn) error {
		res, err := c.Query(`select count(*) from alert_notifications where status='pending'`)
		if err != nil {
			return err
		}
		cnt = i64(res.Rows[0][0])
		return nil
	})
	return cnt
}

// claim is the joined delivery context for one outbox row.
type claim struct {
	id         int64
	attempts   int
	state      string
	value      float64
	firedAt    time.Time
	ruleName   string
	metric     string
	severity   string
	scope      map[string]string
	kind       string
	envelopeID string
	dedupeKey  string
}

func (d claim) payload() notifyPayload {
	return notifyPayload{
		Occasion:  d.state,
		RuleName:  d.ruleName,
		Metric:    d.metric,
		Severity:  d.severity,
		State:     d.state,
		Value:     d.value,
		Scope:     d.scope,
		DedupeKey: d.dedupeKey,
		FiredAt:   d.firedAt,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
