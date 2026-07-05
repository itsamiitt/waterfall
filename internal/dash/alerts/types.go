// Package alerts is the dashboard's alerting engine (module 12, doc 12 §P6): a 30s evaluator over
// the rollups using the CLOSED metric vocabulary (doc 10 §4), edge-triggered firing/resolved
// episodes deduped by the partial unique index (one open episode per rule+scope), and an
// at-least-once notification OUTBOX delivered by a notifier loop over SSRF-guarded HTTP channels
// (Slack/Teams/Discord/webhook + HMAC) and a guarded SMTP dialer (email).
//
// Gates / invariants:
//   - G1 tenant isolation. Rules/channels/events/budgets are Class-T under FORCE RLS; every access
//     binds the ctx (or per-Tenant system) Principal via db.Store.Tx. tenant/role never come from
//     a request body.
//   - Single-firing invariant. alert_events_one_firing_uq makes "one open episode per rule" a
//     database property; the evaluator INSERTs with ON CONFLICT DO NOTHING (doc 10 §5.2).
//   - Notification dedupe. alert_notifications_pending_dedupe_uq (dedupe_key WHERE status='pending')
//     makes a re-enqueue of the same send occasion impossible (doc 10 §5.4).
//   - SSRF discipline on ALL outbound (webhook + SMTP host): resolve-then-dial private-range denial;
//     HTTP via provider.NewEgressClient, SMTP re-implements the same denylist + explicit deadlines.
//   - Secrets sealed. Channel configs (URLs + secrets) live in secret_envelopes; never logged.
package alerts

import "time"

// Channel is a reusable typed contact point (doc 04 §2.11). The config (URL/secret or SMTP
// settings) is sealed in secret_envelopes and never echoed decrypted.
type Channel struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	ConfigEnvelopeID string    `json:"-"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

// ChannelConfig is the plaintext (sealed at rest) delivery config. HTTP channels carry URL +
// optional Secret (HMAC signing key); email carries the SMTP relay settings.
type ChannelConfig struct {
	URL    string `json:"url,omitempty"`
	Secret string `json:"secret,omitempty"`

	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from,omitempty"`
	To       []string `json:"to,omitempty"`
	StartTLS bool     `json:"starttls,omitempty"`
}

// Rule binds one CLOSED-vocabulary metric to a threshold + window + scope (doc 04 §2.11). Scope is
// a small string map (e.g. {"provider_id":"hunter"}); channels are alert_channels ids.
type Rule struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Metric     string            `json:"metric"`
	Scope      map[string]string `json:"scope,omitempty"`
	Op         string            `json:"op"`
	Threshold  float64           `json:"threshold"`
	WindowS    int               `json:"window_s"`
	CooldownS  int               `json:"cooldown_s"`
	Severity   string            `json:"severity,omitempty"`
	Channels   []string          `json:"channels,omitempty"`
	Enabled    bool              `json:"enabled"`
	MutedUntil *time.Time        `json:"muted_until"`
	CreatedBy  string            `json:"created_by,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// Event is one edge-triggered episode row (firing -> resolved). It is NOT partitioned so the
// single-firing partial unique index can exist (doc 03 §2.4).
type Event struct {
	ID         int64      `json:"id"`
	RuleID     string     `json:"rule_id"`
	State      string     `json:"state"`
	Value      *float64   `json:"value,omitempty"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	NotifiedAt *time.Time `json:"notified_at,omitempty"`
	AckBy      string     `json:"ack_by,omitempty"`
	AckAt      *time.Time `json:"ack_at,omitempty"`
	DedupeKey  string     `json:"dedupe_key"`
}

// TestResult is the response of POST /alerts/rules/{id}/test and channel test-send: the value the
// evaluator computed / the delivery status.
type TestResult struct {
	WouldFire    bool     `json:"would_fire,omitempty"`
	Value        *float64 `json:"value,omitempty"`
	HasData      bool     `json:"has_data,omitempty"`
	Delivered    *bool    `json:"delivered,omitempty"`
	StatusCode   int      `json:"status_code,omitempty"`
	ResponseNote string   `json:"response_note,omitempty"`
}

// occasion is the notification send occasion embedded in the notification-grained dedupe_key
// (doc 10 §5.4). renotify occasions carry a cooldown-bucket suffix so successive renotifies do not
// collide while retries of the same occasion dedupe.
type occasion string

const (
	occFired    occasion = "fired"
	occResolved occasion = "resolved"
)

func occRenotify(bucket int64) occasion {
	return occasion("renotify:" + itoa(bucket))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
