// Package realtime is the dashboard's SSE fan-out seam (ADR-0019, doc 04 §3): the per-instance
// Hub with per-topic 256-event ring buffers and Last-Event-ID replay, the ONE multiplexed
// GET /v1/admin/streams?topics=csv handler, the per-instance read-poller that derives events
// from the database (the guaranteed Source), and the SelfMon store — sole writer of the
// self_monitor snapshot row-set (doc 03 §2.7/§6, migration 0010).
//
// QoS split (doc 04 §3.4, binding): `*.tick` events are coalescible — when a subscriber lags,
// the newest tick per topic wins and intermediate ticks are dropped; `*.changed` / `*.fired` /
// `*.resolved` / `*.progress` events are NEVER silently dropped — a subscriber whose bounded
// buffer overflows on a non-coalescible event is DISCONNECTED (forced close), so the client
// reconnects with Last-Event-ID and replays from the ring (or receives an explicit `reset` on
// ring overflow). Staleness is impossible to miss silently.
//
// Tenant scoping: the v1 stream is operator-scoped platform telemetry (doc 12 §P7 constraint) —
// every payload is an aggregate or an entity id + closed-vocabulary state, never row payloads,
// PII, or secrets. Topic subscription is RBAC-gated per doc 04 §3.2 (topicAllowed).
package realtime

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Topics — the closed SINGULAR topic vocabulary (doc 04 §3.2 / OI-API-2): the topic IS the
// first segment of the event name. Served by GET /v1/admin/meta/enums for parity testing.
const (
	TopicOverview = "overview"
	TopicProvider = "provider"
	TopicKey      = "key"
	TopicQueue    = "queue"
	TopicWorker   = "worker"
	TopicAlert    = "alert"
	TopicImport   = "import"
	TopicApproval = "approval"
)

// Topics is the closed topic list in stable order (meta/enums parity).
var Topics = []string{
	TopicOverview, TopicProvider, TopicKey, TopicQueue,
	TopicWorker, TopicAlert, TopicImport, TopicApproval,
}

// EventNames is the closed event-name vocabulary `<domain>.<entity>.<verb>` (doc 04 §3.2 /
// ADR-0019). The first segment IS the topic. New verbs are additive doc-first changes.
var EventNames = []string{
	"overview.tiles.tick",
	"provider.health.changed",
	"key.status.changed",
	"queue.stats.tick",
	"worker.state.changed",
	"alert.event.fired",
	"alert.event.resolved",
	"import.batch.progress",
	"approval.request.changed",
}

var topicSet = func() map[string]bool {
	m := make(map[string]bool, len(Topics))
	for _, t := range Topics {
		m[t] = true
	}
	return m
}()

// ValidTopic reports whether t is in the closed topic vocabulary.
func ValidTopic(t string) bool { return topicSet[t] }

// ID is an SSE event id `<epochms>-<seq>` (doc 04 §3.3) — monotonic per stream; seq
// disambiguates same-millisecond events. The zero ID orders before every real id.
type ID struct {
	Ms  int64
	Seq uint64
}

// String renders the wire form, e.g. "1782996004380-2".
func (id ID) String() string {
	return strconv.FormatInt(id.Ms, 10) + "-" + strconv.FormatUint(id.Seq, 10)
}

// IsZero reports whether the id is unset.
func (id ID) IsZero() bool { return id.Ms == 0 && id.Seq == 0 }

// Less orders ids lexicographically on (epochms, seq).
func (id ID) Less(o ID) bool {
	if id.Ms != o.Ms {
		return id.Ms < o.Ms
	}
	return id.Seq < o.Seq
}

// ParseID parses the wire form of an event id. ok=false for anything malformed (a client
// presenting garbage Last-Event-ID is treated as having no id).
func ParseID(s string) (ID, bool) {
	i := strings.IndexByte(s, '-')
	if i <= 0 || i == len(s)-1 {
		return ID{}, false
	}
	ms, err1 := strconv.ParseInt(s[:i], 10, 64)
	seq, err2 := strconv.ParseUint(s[i+1:], 10, 64)
	if err1 != nil || err2 != nil || ms < 0 {
		return ID{}, false
	}
	return ID{Ms: ms, Seq: seq}, true
}

// Event is one SSE event. Name is from the closed vocabulary; Scope identifies the entity for
// client cache routing (ids only — never PII); Payload is the event-specific body (aggregates
// or closed-vocabulary states only). ID and TS are stamped by the Hub on Publish.
type Event struct {
	Name    string            // e.g. "key.status.changed" — first segment is the topic
	Scope   map[string]string // entity identifiers for cache routing
	Payload any               // JSON-marshaled into the data envelope's "payload"
	TS      time.Time         // server emission time (UTC); stamped by Publish if zero
	ID      ID                // stamped by Publish
}

// Topic returns the event's topic (the first dot segment of Name).
func (e Event) Topic() string {
	if i := strings.IndexByte(e.Name, '.'); i > 0 {
		return e.Name[:i]
	}
	return e.Name
}

// Coalescible reports the QoS class (doc 04 §3.4): `*.tick` events replace snapshots and may
// be coalesced under load; everything else carries invalidation semantics and is never dropped.
func (e Event) Coalescible() bool { return strings.HasSuffix(e.Name, ".tick") }

// Source is the fan-out seam (ADR-0019): Subscribe returns a channel delivering the subscribed
// topics' events in publish order plus a cancel func. The channel is CLOSED by the source when
// the subscriber is too slow for a non-coalescible event (close-don't-drop, doc 04 §3.5) or on
// cancel; an SSE handler treats close as a forced disconnect (client replays via Last-Event-ID).
type Source interface {
	Subscribe(topics []string) (<-chan Event, func())
}

// envelope is the wire `data:` document {"v":1,ts,scope,payload} (doc 04 §3.3).
type envelope struct {
	V       int               `json:"v"`
	TS      string            `json:"ts"`
	Scope   map[string]string `json:"scope"`
	Payload any               `json:"payload"`
}

func envelopeFor(e Event) envelope {
	scope := e.Scope
	if scope == nil {
		scope = map[string]string{}
	}
	return envelope{V: 1, TS: e.TS.UTC().Format(time.RFC3339), Scope: scope, Payload: e.Payload}
}

// String implements a compact debug rendering (never logged with payloads).
func (e Event) String() string { return fmt.Sprintf("%s@%s", e.Name, e.ID) }
