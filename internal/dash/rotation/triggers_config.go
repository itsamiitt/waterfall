package rotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Trigger-config sentinels (mapped to 422 by the HTTP layer).
var (
	// ErrInvalidTrigger reports an unknown trigger kind or malformed thresholds.
	ErrInvalidTrigger = errors.New("rotation: invalid trigger configuration")
	// ErrAuthTriggerImmutable reports an attempt to disable AUTH handling (never permitted: a
	// possibly-compromised key must always park — migration 0005 rotation_triggers note).
	ErrAuthTriggerImmutable = errors.New("rotation: the auth trigger cannot be disabled")
)

// defaultTriggers are the in-code defaults returned for any trigger kind lacking a persisted row
// (migration 0005: "missing row = in-code default"). cooldown_s applies to the auto-recover path.
func defaultTriggers() []TriggerRow {
	cd := func(n int64) *int64 { return &n }
	return []TriggerRow{
		{Trigger: "quota", Thresholds: `{"consecutive":1}`, CooldownS: cd(300), Enabled: true},
		{Trigger: "rate_limit", Thresholds: `{"sustained":3}`, CooldownS: cd(60), Enabled: true},
		{Trigger: "auth", Thresholds: `{"consecutive":1}`, CooldownS: cd(0), Enabled: true},
		{Trigger: "timeout", Thresholds: `{"threshold":5}`, CooldownS: cd(120), Enabled: true},
	}
}

// ListTriggers returns every trigger kind's effective config: the persisted row when present, else
// the in-code default.
func (e *Engine) ListTriggers(ctx context.Context) ([]TriggerRow, error) {
	rows, err := e.store.ListTriggers(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]TriggerRow, len(rows))
	for _, r := range rows {
		byName[r.Trigger] = r
	}
	out := make([]TriggerRow, 0, len(triggerKinds))
	for _, d := range defaultTriggers() {
		if r, ok := byName[d.Trigger]; ok {
			out = append(out, r)
		} else {
			out = append(out, d)
		}
	}
	return out, nil
}

// PutTrigger validates and upserts one trigger row (last-write-wins), auditing before/after. The
// AUTH trigger may be re-tuned but never disabled.
func (e *Engine) PutTrigger(ctx context.Context, tr TriggerRow) (TriggerRow, error) {
	if !validTriggerKind(tr.Trigger) {
		return TriggerRow{}, fmt.Errorf("%w: unknown trigger %q", ErrInvalidTrigger, tr.Trigger)
	}
	if tr.Trigger == "auth" && !tr.Enabled {
		return TriggerRow{}, ErrAuthTriggerImmutable
	}
	if tr.Thresholds != "" && !json.Valid([]byte(tr.Thresholds)) {
		return TriggerRow{}, fmt.Errorf("%w: thresholds is not valid JSON", ErrInvalidTrigger)
	}
	before, _, err := e.store.GetTrigger(ctx, tr.Trigger)
	if err != nil {
		return TriggerRow{}, err
	}
	if err := e.store.UpsertTrigger(ctx, tr, actorFrom(ctx)); err != nil {
		return TriggerRow{}, err
	}
	after, _, err := e.store.GetTrigger(ctx, tr.Trigger)
	if err != nil {
		return TriggerRow{}, err
	}
	e.auditTrigger(ctx, tr.Trigger, before, after)
	return after, nil
}

// auditTrigger appends a redacted before/after audit row for a trigger write. Best-effort (logged,
// not fatal), matching the keys/providers pattern.
func (e *Engine) auditTrigger(ctx context.Context, kind string, before, after TriggerRow) {
	if e.audit == nil {
		return
	}
	entry := audit.Entry{
		Action:     "rotation_trigger_update",
		ObjectKind: "rotation_triggers",
		ObjectID:   kind,
		Before:     triggerSnapshot(before),
		After:      triggerSnapshot(after),
	}
	if p, err := tenant.FromContext(ctx); err == nil {
		entry.ActorUserID = p.UserID
		entry.ActorRole = db.RoleFromPrincipal(p)
	}
	if err := e.audit.Append(ctx, entry); err != nil {
		e.log.Error("rotation: audit trigger update failed", "trigger", kind, "err", err)
	}
}

func triggerSnapshot(tr TriggerRow) json.RawMessage {
	m := map[string]string{
		"trigger":    tr.Trigger,
		"thresholds": tr.Thresholds,
		"enabled":    fmt.Sprint(tr.Enabled),
	}
	if tr.CooldownS != nil {
		m["cooldown_s"] = fmt.Sprint(*tr.CooldownS)
	}
	return jraw(m)
}

func actorFrom(ctx context.Context) string {
	if p, err := tenant.FromContext(ctx); err == nil {
		return p.UserID
	}
	return ""
}
