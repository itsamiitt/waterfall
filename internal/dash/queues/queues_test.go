package queues

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveState(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Second)
	stale := now.Add(-90 * time.Second)
	cases := []struct {
		name          string
		pending, dead bool
		claimed       *time.Time
		attempts      int
		status        string
		want          State
	}{
		{"waiting", true, false, nil, 0, "queued", StateWaiting},
		{"running", true, false, &fresh, 1, "running", StateRunning},
		{"retry-unclaimed", true, false, nil, 2, "queued", StateRetry},
		{"retry-stale-claim", true, false, &stale, 3, "queued", StateRetry},
		{"failed", false, false, nil, 1, "failed", StateFailed},
		{"dead", true, true, nil, 5, "queued", StateDead},
	}
	for _, c := range cases {
		got := deriveState(c.pending, c.dead, c.claimed, c.attempts, c.status, now, defaultVisibilitySeconds)
		if got != c.want {
			t.Fatalf("%s: deriveState = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStatePredicate(t *testing.T) {
	if p := statePredicate(StateScheduled, 30); p != "false" {
		t.Fatalf("scheduled is always 0 on pgoutbox, got %q", p)
	}
	if p := statePredicate(StateDead, 30); p != "dead" {
		t.Fatalf("dead predicate = %q", p)
	}
	if p := statePredicate(StateRunning, 30); !strings.Contains(p, "claimed_at >=") {
		t.Fatalf("running predicate missing visibility window: %q", p)
	}
	if p := statePredicate(StateWaiting, 30); !strings.Contains(p, "attempts = 0") {
		t.Fatalf("waiting predicate = %q", p)
	}
}

func TestDeadWhere(t *testing.T) {
	where, args := deadWhere(DeadFilter{ErrorClass: "TRANSIENT"})
	if !strings.Contains(where, "last_error ilike $1") || len(args) != 1 {
		t.Fatalf("error_class filter not applied: %q args=%v", where, args)
	}
	where2, args2 := deadWhere(DeadFilter{})
	if where2 != "dead" || len(args2) != 0 {
		t.Fatalf("empty filter = %q args=%v", where2, args2)
	}
}

func TestTokenBucketDoesNotBlockSmallReplay(t *testing.T) {
	now := time.Now()
	tb := newTokenBucket(600, func() time.Time { return now })
	start := time.Now()
	for i := 0; i < 100; i++ {
		tb.take()
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("100 redrives under a 600/min bucket must not block, took %v", elapsed)
	}
}

func TestUUIDV4Format(t *testing.T) {
	id := uuidV4()
	if !looksLikeUUID(id) {
		t.Fatalf("uuidV4 produced non-uuid %q", id)
	}
	if id[14] != '4' {
		t.Fatalf("uuidV4 version nibble not 4: %q", id)
	}
}
