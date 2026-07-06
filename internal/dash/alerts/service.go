package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// ErrUnknownChannel is returned when a rule references a channel that does not exist for the Tenant.
var ErrUnknownChannel = errors.New("alerts: rule references an unknown channel")

// ErrInvalidChannelKind is returned for a channel kind outside the CHECK enum.
var ErrInvalidChannelKind = errors.New("alerts: channel kind must be email|slack|teams|discord|webhook")

var validChannelKinds = map[string]bool{
	"email": true, "slack": true, "teams": true, "discord": true, "webhook": true,
}

// Service is the alerts CRUD engine behind the HTTP surface. It composes the Store (RLS-scoped
// persistence), the envelope Secrets backend (channel configs sealed at rest), the audit Log, and
// the Evaluator/Notifier for the two "test now" endpoints. It holds no tenant state.
type Service struct {
	store    *Store
	secrets  Secrets
	audit    auditor
	eval     *Evaluator
	notifier *Notifier
	now      func() time.Time
}

// Config bundles the Service collaborators.
type Config struct {
	Store    *Store
	Secrets  Secrets
	Audit    auditor
	Eval     *Evaluator
	Notifier *Notifier
	Now      func() time.Time
}

// NewService builds a Service.
func NewService(cfg Config) *Service {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: cfg.Store, secrets: cfg.Secrets, audit: cfg.Audit,
		eval: cfg.Eval, notifier: cfg.Notifier, now: now,
	}
}

// --- rules ---

// CreateRule validates the metric vocabulary + scope + referenced channels, persists the rule, and
// audits the create. created_by comes from the ctx Principal.
func (s *Service) CreateRule(ctx context.Context, r Rule) (Rule, error) {
	if err := validateRule(r); err != nil {
		return Rule{}, err
	}
	if err := s.checkChannels(ctx, r.Channels); err != nil {
		return Rule{}, err
	}
	if p, err := tenant.FromContext(ctx); err == nil {
		r.CreatedBy = p.UserID
	}
	out, err := s.store.InsertRule(ctx, r)
	if err != nil {
		return Rule{}, err
	}
	s.auditWrite(ctx, "alert_rule_create", out.ID, nil, ruleSnapshot(out))
	return out, nil
}

// ListRules lists the Tenant's rules with optional filters.
func (s *Service) ListRules(ctx context.Context, metric, severity string, enabled *bool) ([]Rule, error) {
	return s.store.ListRules(ctx, metric, severity, enabled)
}

// GetRule returns one rule.
func (s *Service) GetRule(ctx context.Context, id string) (Rule, error) {
	return s.store.GetRule(ctx, id)
}

// PatchRule applies a partial update (mutes are audited). Channels, if provided, are validated.
func (s *Service) PatchRule(ctx context.Context, id string, p RulePatch) (Rule, error) {
	if p.Channels != nil {
		if err := s.checkChannels(ctx, p.Channels); err != nil {
			return Rule{}, err
		}
	}
	before, err := s.store.GetRule(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	out, err := s.store.PatchRule(ctx, id, p)
	if err != nil {
		return Rule{}, err
	}
	action := "alert_rule_update"
	if p.SetMuted {
		action = "alert_rule_mute"
	}
	s.auditWrite(ctx, action, id, ruleSnapshot(before), ruleSnapshot(out))
	return out, nil
}

// DeleteRule removes a rule (open episodes auto-resolve) and audits it.
func (s *Service) DeleteRule(ctx context.Context, id string) error {
	before, err := s.store.GetRule(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteRule(ctx, id); err != nil {
		return err
	}
	s.auditWrite(ctx, "alert_rule_delete", id, ruleSnapshot(before), nil)
	return nil
}

// TestRule evaluates a rule against current rollups WITHOUT notifying (doc 04 §2.11). It returns the
// computed value and whether the rule would fire.
func (s *Service) TestRule(ctx context.Context, id string) (TestResult, error) {
	r, err := s.store.GetRule(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return TestResult{}, err
	}
	mr, err := s.eval.readMetric(ctx, p.TenantID, r, s.now().UTC())
	if err != nil {
		return TestResult{}, err
	}
	v := mr.value
	return TestResult{WouldFire: mr.breaching, Value: &v, HasData: mr.hasData}, nil
}

// --- channels ---

// CreateChannel seals the config to secret_envelopes (kind channel_config) and persists the channel
// row. The plaintext config never touches the channel table or the audit log (only the envelope id).
func (s *Service) CreateChannel(ctx context.Context, kind, name string, cfg ChannelConfig) (Channel, error) {
	if !validChannelKinds[kind] {
		return Channel{}, ErrInvalidChannelKind
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return Channel{}, err
	}
	envID, err := s.secrets.Seal(ctx, "channel_config", raw)
	if err != nil {
		return Channel{}, err
	}
	ch, err := s.store.InsertChannel(ctx, Channel{Kind: kind, Name: name, ConfigEnvelopeID: string(envID)})
	if err != nil {
		return Channel{}, err
	}
	s.auditWrite(ctx, "alert_channel_create", ch.ID, nil, channelSnapshot(ch))
	return ch, nil
}

// ListChannels lists the Tenant's channels (config never echoed decrypted).
func (s *Service) ListChannels(ctx context.Context) ([]Channel, error) {
	return s.store.ListChannels(ctx)
}

// DeleteChannel removes a channel (409 if referenced by an enabled rule) and audits it.
func (s *Service) DeleteChannel(ctx context.Context, id string) error {
	if err := s.store.DeleteChannel(ctx, id); err != nil {
		return err
	}
	s.auditWrite(ctx, "alert_channel_delete", id, nil, nil)
	return nil
}

// TestChannel exercises the real notifier delivery path (identical builder + SSRF guard).
func (s *Service) TestChannel(ctx context.Context, id string) (TestResult, error) {
	return s.notifier.TestSend(ctx, id)
}

// --- events ---

// ListEvents returns episode history for the Tenant.
func (s *Service) ListEvents(ctx context.Context, state, ruleID, severity string, from, to *time.Time, limit int) ([]Event, error) {
	return s.store.ListEvents(ctx, state, ruleID, severity, from, to, limit)
}

// AckEvent acknowledges a firing episode and audits it.
func (s *Service) AckEvent(ctx context.Context, id int64) (Event, error) {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return Event{}, err
	}
	ev, err := s.store.AckEvent(ctx, id, p.UserID)
	if err != nil {
		return Event{}, err
	}
	s.auditWrite(ctx, "alert_event_ack", itoa(id), nil, nil)
	return ev, nil
}

// --- helpers ---

// checkChannels verifies every referenced channel id exists for the ctx Tenant.
func (s *Service) checkChannels(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.store.db.Tx(ctx, func(c *pg.Conn) error {
		for _, id := range ids {
			res, err := c.QueryParams(`select 1 from alert_channels where id = $1`, id)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				return ErrUnknownChannel
			}
		}
		return nil
	})
}

// auditWrite is a best-effort audit append (nil-safe). Snapshots redact secrets (envelope ids only).
func (s *Service) auditWrite(ctx context.Context, action, objectID string, before, after json.RawMessage) {
	if s.audit == nil {
		return
	}
	p, _ := tenant.FromContext(ctx)
	_ = s.audit.Append(ctx, audit.Entry{
		Action: action, ObjectKind: "alert", ObjectID: objectID,
		ActorUserID: p.UserID, Before: before, After: after,
	})
}

func ruleSnapshot(r Rule) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"id": r.ID, "name": r.Name, "metric": r.Metric, "op": r.Op, "threshold": r.Threshold,
		"window_s": r.WindowS, "cooldown_s": r.CooldownS, "enabled": r.Enabled, "channels": r.Channels,
		"anomaly_floor_credits": r.AnomalyFloorCredits,
	})
	return b
}

func channelSnapshot(ch Channel) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"id": ch.ID, "kind": ch.Kind, "name": ch.Name, "config_envelope_id": ch.ConfigEnvelopeID,
	})
	return b
}
