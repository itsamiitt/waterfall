package alerts

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Sentinel errors (wrapped %w by callers; the HTTP layer maps them to status codes).
var (
	ErrNotFound   = errors.New("alerts: not found")
	ErrChannelRef = errors.New("alerts: channel is referenced by an enabled rule")
)

// txRunner is the consumer-side slice of db.Store the alerts package needs: dual-GUC RLS
// transactions bound to the ctx Principal (Tx) and the platform system Principal (PlatformTx).
// Satisfied by *db.Store.
type txRunner interface {
	Tx(ctx context.Context, fn func(*pg.Conn) error) error
	PlatformTx(ctx context.Context, fn func(*pg.Conn) error) error
}

var _ txRunner = (*db.Store)(nil)

// Store is the alerts persistence seam over db.Store. It exposes the CRUD the HTTP service needs
// plus the low-level per-Tenant binding helpers the evaluator and notifier loops use to run
// RLS-scoped work for a Tenant that is not a request Principal.
type Store struct {
	db  txRunner
	now func() time.Time
}

// NewStore builds a Store. now may be nil (wall clock).
func NewStore(d txRunner, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: d, now: now}
}

// tenantTx runs fn under a tenant-bound RLS transaction for tid using a synthetic system Principal
// with role tenant_admin — deliberately NOT operator, so only the tenant_isolation policy admits
// rows and an operator cross-tenant SELECT policy can never widen a Class-T read past tid (G1). Used
// by the evaluator loop (no request Principal) for reading a Tenant's rules and writing its
// episodes/notifications.
func (s *Store) tenantTx(ctx context.Context, tid string, fn func(*pg.Conn) error) error {
	return s.db.Tx(principalCtx(ctx, tid), fn)
}

// principalCtx binds a synthetic system Principal for tid with role tenant_admin — a within-tenant
// binding that admits only the tenant_isolation policy (no operator cross-tenant widening), so
// Class-T reads/writes stay scoped to tid (G1).
func principalCtx(ctx context.Context, tid string) context.Context {
	return tenant.WithPrincipal(ctx, tenant.Principal{
		TenantID: tid, UserID: "system", Scopes: []string{"role:tenant_admin"},
	})
}

// platformTx runs fn under the platform system Principal (Class-P source rollups: provider/key/
// queue/worker stats and providers.op_state).
func (s *Store) platformTx(ctx context.Context, fn func(*pg.Conn) error) error {
	return s.db.PlatformTx(ctx, fn)
}

// activeTenantIDs lists the customer Tenants plus 'platform' the evaluator sweeps (operator rules
// on system.* metrics live under 'platform'). Read under PlatformTx from the operator-readable
// tenants registry.
func (s *Store) activeTenantIDs(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.platformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query("select id from tenants where status = 'active'")
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			if r[0] != nil {
				ids = append(ids, *r[0])
			}
		}
		return nil
	})
	return ids, err
}

// --- rule CRUD (HTTP service) ---

// InsertRule persists a new rule under the ctx Principal's Tenant (G1: tenant_id from the bound
// session, never the body).
func (s *Store) InsertRule(ctx context.Context, r Rule) (Rule, error) {
	r.ID = newUUID()
	r.UpdatedAt = s.now().UTC()
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into alert_rules
			   (id, tenant_id, name, metric, scope, op, threshold, window_s, cooldown_s,
			    severity, channels, enabled, muted_until, created_by, updated_at)
			 values ($1, app_current_tenant(), $2, $3, $4::jsonb, $5, $6, $7, $8, $9,
			         $10::uuid[], $11, $12, $13, $14)`,
			r.ID, r.Name, r.Metric, scopeJSON(r.Scope), r.Op, r.Threshold, r.WindowS, r.CooldownS,
			nullStr(r.Severity), uuidArrayLiteral(r.Channels), r.Enabled, tsOrNull(r.MutedUntil),
			nullStr(r.CreatedBy), r.UpdatedAt)
	})
	if err != nil {
		return Rule{}, err
	}
	return r, nil
}

// ListRules returns the Tenant's rules, optionally filtered by metric/severity/enabled.
func (s *Store) ListRules(ctx context.Context, fMetric, fSeverity string, fEnabled *bool) ([]Rule, error) {
	var out []Rule
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		q := `select id, name, metric, scope, op, threshold, window_s, cooldown_s, severity,
		             channels, enabled, muted_until, created_by, updated_at
		        from alert_rules where 1=1`
		var args []any
		if fMetric != "" {
			args = append(args, fMetric)
			q += " and metric = $" + strconv.Itoa(len(args))
		}
		if fSeverity != "" {
			args = append(args, fSeverity)
			q += " and severity = $" + strconv.Itoa(len(args))
		}
		if fEnabled != nil {
			args = append(args, *fEnabled)
			q += " and enabled = $" + strconv.Itoa(len(args))
		}
		q += " order by updated_at desc"
		res, qerr := c.QueryParams(q, args...)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			out = append(out, scanRule(r))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetRule returns one rule by id, ErrNotFound if absent (or another Tenant's, which RLS hides).
func (s *Store) GetRule(ctx context.Context, id string) (Rule, error) {
	var out Rule
	found := false
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select id, name, metric, scope, op, threshold, window_s, cooldown_s, severity,
			        channels, enabled, muted_until, created_by, updated_at
			   from alert_rules where id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) > 0 {
			out = scanRule(res.Rows[0])
			found = true
		}
		return nil
	})
	if err != nil {
		return Rule{}, err
	}
	if !found {
		return Rule{}, ErrNotFound
	}
	return out, nil
}

// PatchRule updates the mutable fields of a rule (name, threshold, window, cooldown, severity,
// channels, enabled, muted_until). Fields left nil are unchanged. Returns the updated rule.
func (s *Store) PatchRule(ctx context.Context, id string, p RulePatch) (Rule, error) {
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id from alert_rules where id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		return c.ExecParams(
			`update alert_rules set
			   name        = coalesce($2, name),
			   threshold   = coalesce($3, threshold),
			   window_s    = coalesce($4, window_s),
			   cooldown_s  = coalesce($5, cooldown_s),
			   severity    = coalesce($6, severity),
			   channels    = coalesce($7::uuid[], channels),
			   enabled     = coalesce($8, enabled),
			   muted_until = case when $9 then $10 else muted_until end,
			   updated_at  = $11
			 where id = $1`,
			id, p.Name, p.Threshold, p.WindowS, p.CooldownS, p.Severity,
			uuidArrayOrNull(p.Channels), p.Enabled, p.SetMuted, tsOrNull(p.MutedUntil), s.now().UTC())
	})
	if err != nil {
		return Rule{}, err
	}
	return s.GetRule(ctx, id)
}

// DeleteRule removes a rule and auto-resolves its open episodes (doc 04 §2.11).
func (s *Store) DeleteRule(ctx context.Context, id string) error {
	return s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id from alert_rules where id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		if err := c.ExecParams(
			`update alert_events set state='resolved', resolved_at=$2
			   where rule_id=$1 and state='firing'`, id, s.now().UTC()); err != nil {
			return err
		}
		return c.ExecParams(`delete from alert_rules where id = $1`, id)
	})
}

// RulePatch carries the optional fields of a PATCH. A nil pointer means "leave unchanged"; SetMuted
// distinguishes "clear muted_until" (SetMuted=true, MutedUntil=nil) from "leave it".
type RulePatch struct {
	Name       *string
	Threshold  *float64
	WindowS    *int
	CooldownS  *int
	Severity   *string
	Channels   []string
	Enabled    *bool
	SetMuted   bool
	MutedUntil *time.Time
}

// --- channel CRUD ---

// InsertChannel persists a channel row whose config was already sealed to secret_envelopes.
func (s *Store) InsertChannel(ctx context.Context, ch Channel) (Channel, error) {
	ch.ID = newUUID()
	ch.CreatedAt = s.now().UTC()
	if ch.Status == "" {
		ch.Status = "active"
	}
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into alert_channels (id, tenant_id, kind, name, config_envelope_id, status, created_at)
			 values ($1, app_current_tenant(), $2, $3, $4, $5, $6)`,
			ch.ID, ch.Kind, ch.Name, ch.ConfigEnvelopeID, ch.Status, ch.CreatedAt)
	})
	if err != nil {
		return Channel{}, err
	}
	return ch, nil
}

// ListChannels returns the Tenant's channels (config envelope id withheld from JSON).
func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	var out []Channel
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query(
			`select id, kind, name, config_envelope_id, status, created_at
			   from alert_channels order by created_at desc`)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			out = append(out, scanChannel(r))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// getChannel loads one channel (including its envelope id) for the ctx Tenant.
func (s *Store) getChannel(ctx context.Context, id string) (Channel, error) {
	var out Channel
	found := false
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select id, kind, name, config_envelope_id, status, created_at
			   from alert_channels where id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) > 0 {
			out = scanChannel(res.Rows[0])
			found = true
		}
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	if !found {
		return Channel{}, ErrNotFound
	}
	return out, nil
}

// DeleteChannel removes a channel, refusing (ErrChannelRef -> 409) if an enabled rule references it.
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	return s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id from alert_channels where id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		ref, rerr := c.QueryParams(
			`select 1 from alert_rules where enabled = true and $1 = any(channels) limit 1`, id)
		if rerr != nil {
			return rerr
		}
		if len(ref.Rows) > 0 {
			return ErrChannelRef
		}
		return c.ExecParams(`delete from alert_channels where id = $1`, id)
	})
}

// --- event history ---

// ListEvents returns episode history newest-first, filtered by state/rule/severity/window.
func (s *Store) ListEvents(ctx context.Context, fState, fRuleID, fSeverity string, from, to *time.Time, limit int) ([]Event, error) {
	limit = db.ClampLimit(limit)
	var out []Event
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		q := `select e.id, e.rule_id, e.state, e.value, e.fired_at, e.resolved_at, e.notified_at,
		             e.ack_by, e.ack_at, e.dedupe_key
		        from alert_events e`
		var args []any
		join := ""
		where := " where 1=1"
		if fSeverity != "" {
			join = " join alert_rules r on r.id = e.rule_id"
			args = append(args, fSeverity)
			where += " and r.severity = $" + strconv.Itoa(len(args))
		}
		if fState != "" {
			args = append(args, fState)
			where += " and e.state = $" + strconv.Itoa(len(args))
		}
		if fRuleID != "" {
			args = append(args, fRuleID)
			where += " and e.rule_id = $" + strconv.Itoa(len(args))
		}
		if from != nil {
			args = append(args, from.UTC())
			where += " and e.fired_at >= $" + strconv.Itoa(len(args))
		}
		if to != nil {
			args = append(args, to.UTC())
			where += " and e.fired_at < $" + strconv.Itoa(len(args))
		}
		args = append(args, int64(limit))
		q += join + where + " order by e.fired_at desc limit $" + strconv.Itoa(len(args))
		res, qerr := c.QueryParams(q, args...)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			out = append(out, scanEvent(r))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AckEvent marks a firing episode acknowledged (suppresses renotify; resolve still notifies,
// doc 10 §5.3). Returns ErrNotFound if the event is absent for the Tenant.
func (s *Store) AckEvent(ctx context.Context, id int64, userID string) (Event, error) {
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select id from alert_events where id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		return c.ExecParams(
			`update alert_events set ack_by = $2, ack_at = $3 where id = $1 and state = 'firing'`,
			id, nullStr(userID), s.now().UTC())
	})
	if err != nil {
		return Event{}, err
	}
	return s.getEvent(ctx, id)
}

func (s *Store) getEvent(ctx context.Context, id int64) (Event, error) {
	var out Event
	found := false
	err := s.db.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select id, rule_id, state, value, fired_at, resolved_at, notified_at, ack_by, ack_at, dedupe_key
			   from alert_events where id = $1`, id)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) > 0 {
			out = scanEvent(res.Rows[0])
			found = true
		}
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	if !found {
		return Event{}, ErrNotFound
	}
	return out, nil
}
