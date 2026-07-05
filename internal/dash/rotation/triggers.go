package rotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/domain"
)

// State is the KM-3 Provider Key state machine's state (doc 07 §9). It is the provider_keys.status
// CHECK vocabulary PLUS the virtual "probing" state, which is NOT a status enum value — it is
// persisted as status='exhausted' + health='probing' (OI-RW-1); the diagram shows it as a distinct
// state for operator comprehension and so the transition table can encode its edges.
type State string

const (
	StateActive      State = keys.StatusActive
	StatePaused      State = keys.StatusPaused
	StateExhausted   State = keys.StatusExhausted
	StateProbing     State = "probing" // virtual: persisted as exhausted + health=probing
	StateRateLimited State = keys.StatusRateLimited
	StateAuthFailed  State = keys.StatusAuthFailed
	StateDisabled    State = keys.StatusDisabled
	StateExpired     State = keys.StatusExpired
	StateRotating    State = keys.StatusRotating
	StateArchived    State = keys.StatusArchived
)

// ErrIllegalTransition is the sentinel for a transition not in the KM-3 legal-edge table. The HTTP
// layer maps it to 409; callers wrap it with %w.
var ErrIllegalTransition = errors.New("rotation: illegal key state transition")

// Transitions is the KM-3 legal-edge table (doc 07 §9 stateDiagram + trigger mapping). An edge
// absent here is illegal and Apply rejects it with ErrIllegalTransition. "Manual archive: any ->
// archived" and "rotation initiated: any -> rotating" from the §9 trigger table are encoded as
// edges from every non-terminal state; archived is terminal (no outgoing edges).
var Transitions = map[State]map[State]bool{
	StateActive:      {StatePaused: true, StateExhausted: true, StateRateLimited: true, StateAuthFailed: true, StateExpired: true, StateRotating: true, StateArchived: true},
	StatePaused:      {StateActive: true, StateRotating: true, StateArchived: true},
	StateExhausted:   {StateProbing: true, StateRotating: true, StateArchived: true},
	StateProbing:     {StateActive: true, StateExhausted: true, StateArchived: true},
	StateRateLimited: {StateActive: true, StateRotating: true, StateArchived: true},
	StateAuthFailed:  {StateDisabled: true, StateArchived: true},
	StateDisabled:    {StateActive: true, StateRotating: true, StateArchived: true},
	StateExpired:     {StateRotating: true, StateArchived: true},
	StateRotating:    {StateActive: true, StateArchived: true},
	StateArchived:    {}, // terminal
}

// legal reports whether from -> to is a KM-3 legal edge.
func legal(from, to State) bool { return Transitions[from][to] }

// persistCols maps a target State onto the (status, health) columns actually written to
// provider_keys. probing collapses to status='exhausted' + health='probing' (OI-RW-1); every other
// state writes its own status and clears health.
func persistCols(to State) (status, health string) {
	if to == StateProbing {
		return keys.StatusExhausted, "probing"
	}
	return string(to), ""
}

// StatusEvent is the key.status.changed event intent (doc 04 §3.2 / doc 07 §9). The P7 SSE layer
// consumes it; for P2 a no-op / collecting sink is used. It carries snapshots only — never secrets.
type StatusEvent struct {
	KeyID string    `json:"key_id"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	Class string    `json:"class,omitempty"` // triggering error class; "" for manual/rotation edges
	Alert bool      `json:"alert,omitempty"` // AUTH-park emits an alert intent
	At    time.Time `json:"at"`
}

// EventSink receives key.status.changed intents. The P7 realtime layer implements it.
type EventSink interface {
	KeyStatusChanged(StatusEvent)
}

// CollectingSink is a concurrency-safe in-memory EventSink for P2 (and tests). It is the default
// sink until P7's SSE layer is wired.
type CollectingSink struct {
	mu     sync.Mutex
	events []StatusEvent
}

// KeyStatusChanged records ev.
func (s *CollectingSink) KeyStatusChanged(ev StatusEvent) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

// Events returns a copy of the collected events.
func (s *CollectingSink) Events() []StatusEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StatusEvent, len(s.events))
	copy(out, s.events)
	return out
}

// StatusStore persists a KM-3 status transition to provider_keys (Class P). Satisfied by *pgStore.
type StatusStore interface {
	SetKeyStatus(ctx context.Context, keyID, status, health string) error
}

// Auditor is the consumer-side view of the audit hash chain (satisfied by *audit.Log).
type Auditor interface {
	Append(ctx context.Context, e audit.Entry) error
}

// Trigger applies KM-3 transitions: it validates the edge, persists provider_keys.status (+ health
// for probing), appends a redacted audit row, and emits a key.status.changed intent. An illegal
// edge returns ErrIllegalTransition and performs NO side effect (no persist, no audit, no event).
type Trigger struct {
	store StatusStore
	audit Auditor
	sink  EventSink
	now   func() time.Time
	log   *slog.Logger
}

// newTrigger builds a Trigger. sink defaults to a collecting sink; now to time.Now.
func newTrigger(store StatusStore, aud Auditor, sink EventSink, now func() time.Time, log *slog.Logger) *Trigger {
	if sink == nil {
		sink = &CollectingSink{}
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Trigger{store: store, audit: aud, sink: sink, now: now, log: log}
}

// Apply performs one KM-3 transition from -> to. class is the triggering error-class string ("" for
// manual/rotation edges) and alert marks an alert intent (AUTH park). Legal edges persist + audit +
// emit; illegal edges return ErrIllegalTransition untouched.
func (tr *Trigger) Apply(ctx context.Context, keyID string, from, to State, class string, alert bool) error {
	if !legal(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	status, health := persistCols(to)
	if err := tr.store.SetKeyStatus(ctx, keyID, status, health); err != nil {
		return err
	}
	// Audit is best-effort (logged, not fatal — matching the keys/providers appendAudit pattern);
	// snapshots are string/enum only so the hash chain re-canonicalizes identically.
	if tr.audit != nil {
		e := audit.Entry{
			Action:     "key_status_changed",
			ObjectKind: "provider_keys",
			ObjectID:   keyID,
			ActorRole:  "system",
			Before:     jraw(map[string]string{"status": string(from)}),
			After:      jraw(map[string]string{"status": string(to), "class": class}),
		}
		if err := tr.audit.Append(ctx, e); err != nil {
			tr.log.Error("rotation: audit append failed", "key_id", keyID, "err", err)
		}
	}
	tr.sink.KeyStatusChanged(StatusEvent{
		KeyID: keyID, From: string(from), To: string(to), Class: class, Alert: alert, At: tr.now().UTC(),
	})
	return nil
}

// transitionForClass maps an 8-class Outcome class to the KM-3 target state from the key's current
// state, per the doc 07 §9 trigger table. ok=false means "no key transition for this class" (e.g.
// PROVIDER_DOWN opens a provider breaker, not a key transition; NOT_FOUND/BAD_REQUEST are terminal
// non-events). The sustained-RATE_LIMIT threshold is applied by the engine before it calls Apply.
func transitionForClass(class domain.ErrorClass, from State) (to State, alert, ok bool) {
	if from != StateActive {
		return "", false, false // triggers only fire from the serving (active) state
	}
	switch class {
	case domain.ClassQuota:
		return StateExhausted, false, true
	case domain.ClassRateLimit:
		return StateRateLimited, false, true
	case domain.ClassAuth:
		return StateAuthFailed, true, true // then auth_failed -> disabled (engine chains it)
	default:
		return "", false, false
	}
}

func jraw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
