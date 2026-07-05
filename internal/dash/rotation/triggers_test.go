package rotation

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/domain"
)

// fakeStatusStore records SetKeyStatus calls.
type fakeStatusStore struct {
	mu     sync.Mutex
	calls  int
	status string
	health string
}

func (f *fakeStatusStore) SetKeyStatus(_ context.Context, _, status, health string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.status, f.health = status, health
	return nil
}

// fakeAuditor records audit appends.
type fakeAuditor struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (f *fakeAuditor) Append(_ context.Context, e audit.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

// allStates is every KM-3 state including the virtual probing.
var allStates = []State{
	StateActive, StatePaused, StateExhausted, StateProbing, StateRateLimited,
	StateAuthFailed, StateDisabled, StateExpired, StateRotating, StateArchived,
}

// TestStateMachineTable is P2 acceptance #4 (doc 12): every legal KM-3 transition (doc 07 §9) is
// accepted and persists status + appends audit + emits key.status.changed; every illegal transition
// is rejected with the ErrIllegalTransition sentinel and performs NO side effect.
func TestStateMachineTable(t *testing.T) {
	legalCount, illegalCount := 0, 0
	for _, from := range allStates {
		for _, to := range allStates {
			if from == to {
				continue
			}
			store := &fakeStatusStore{}
			aud := &fakeAuditor{}
			sink := &CollectingSink{}
			tr := newTrigger(store, aud, sink, nil, nil)

			err := tr.Apply(context.Background(), "key-1", from, to, "", false)

			if legal(from, to) {
				legalCount++
				if err != nil {
					t.Fatalf("legal transition %s->%s rejected: %v", from, to, err)
				}
				wantStatus, wantHealth := persistCols(to)
				if store.calls != 1 || store.status != wantStatus || store.health != wantHealth {
					t.Fatalf("legal %s->%s: persisted (calls=%d status=%q health=%q), want (1, %q, %q)",
						from, to, store.calls, store.status, store.health, wantStatus, wantHealth)
				}
				if len(aud.entries) != 1 {
					t.Fatalf("legal %s->%s: appended %d audit rows, want 1", from, to, len(aud.entries))
				}
				if aud.entries[0].Action != "key_status_changed" || aud.entries[0].ObjectKind != "provider_keys" {
					t.Fatalf("legal %s->%s: audit entry shape wrong: %+v", from, to, aud.entries[0])
				}
				ev := sink.Events()
				if len(ev) != 1 || ev[0].From != string(from) || ev[0].To != string(to) {
					t.Fatalf("legal %s->%s: emitted %d events, want 1 matching from/to", from, to, len(ev))
				}
			} else {
				illegalCount++
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("illegal transition %s->%s: got err %v, want ErrIllegalTransition", from, to, err)
				}
				if store.calls != 0 || len(aud.entries) != 0 || len(sink.Events()) != 0 {
					t.Fatalf("illegal %s->%s produced side effects (status=%d audit=%d events=%d)",
						from, to, store.calls, len(aud.entries), len(sink.Events()))
				}
			}
		}
	}
	if legalCount == 0 {
		t.Fatal("no legal transitions were exercised")
	}
	t.Logf("PASS KM-3 state machine: %d legal transitions accepted (+audit +event), %d illegal rejected with sentinel",
		legalCount, illegalCount)
}

// TestProbingPersistedAsExhausted pins OI-RW-1: the virtual probing state persists as
// status='exhausted' + health='probing'.
func TestProbingPersistedAsExhausted(t *testing.T) {
	status, health := persistCols(StateProbing)
	if status != "exhausted" || health != "probing" {
		t.Fatalf("persistCols(probing) = (%q,%q), want (exhausted, probing)", status, health)
	}
}

// TestTransitionForClass pins the 8-class taxonomy -> KM-3 target mapping (doc 07 §9 trigger table).
func TestTransitionForClass(t *testing.T) {
	type tc struct {
		class domain.ErrorClass
		from  State
		to    State
		alert bool
		ok    bool
	}
	cases := []tc{
		{domain.ClassQuota, StateActive, StateExhausted, false, true},
		{domain.ClassRateLimit, StateActive, StateRateLimited, false, true},
		{domain.ClassAuth, StateActive, StateAuthFailed, true, true},
		{domain.ClassProviderDown, StateActive, "", false, false}, // provider breaker, not key state
		{domain.ClassNotFound, StateActive, "", false, false},
		{domain.ClassBadRequest, StateActive, "", false, false},
		{domain.ClassTransient, StateActive, "", false, false},
		{domain.ClassQuota, StatePaused, "", false, false}, // triggers only fire from active
	}
	for _, c := range cases {
		to, alert, ok := transitionForClass(c.class, c.from)
		if ok != c.ok || to != c.to || alert != c.alert {
			t.Fatalf("transitionForClass(%v,%s) = (%s,%v,%v), want (%s,%v,%v)",
				c.class, c.from, to, alert, ok, c.to, c.alert, c.ok)
		}
	}
}
