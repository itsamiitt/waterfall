package realtime

import (
	"fmt"
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Event, within time.Duration) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(within):
		t.Fatalf("timed out waiting for event")
		return Event{}, false
	}
}

func TestHubIDsMonotonicAndParse(t *testing.T) {
	h := NewHub(HubConfig{}, nil)
	var last ID
	for i := 0; i < 1000; i++ {
		id := h.Publish(Event{Name: "provider.health.changed"})
		if !last.Less(id) {
			t.Fatalf("id %v not > %v", id, last)
		}
		back, ok := ParseID(id.String())
		if !ok || back != id {
			t.Fatalf("ParseID(%q) = %v, %v", id.String(), back, ok)
		}
		last = id
	}
	if _, ok := ParseID("garbage"); ok {
		t.Fatal("ParseID accepted garbage")
	}
	if _, ok := ParseID("12-"); ok {
		t.Fatal("ParseID accepted trailing dash")
	}
}

func TestHubDeliversInOrder(t *testing.T) {
	h := NewHub(HubConfig{}, nil)
	ch, cancel := h.Subscribe([]string{"key", "alert"})
	defer cancel()
	var want []ID
	for i := 0; i < 20; i++ {
		want = append(want, h.Publish(Event{Name: "key.status.changed", Scope: map[string]string{"i": fmt.Sprint(i)}}))
		want = append(want, h.Publish(Event{Name: "alert.event.fired"}))
	}
	h.Publish(Event{Name: "worker.state.changed"}) // unsubscribed topic: never delivered
	for i, id := range want {
		e, ok := recv(t, ch, time.Second)
		if !ok {
			t.Fatalf("channel closed at %d", i)
		}
		if e.ID != id {
			t.Fatalf("event %d id = %v, want %v", i, e.ID, id)
		}
	}
}

// TestHubTickCoalesce: a subscriber that has not drained yet sees only the LATEST tick per
// topic (latest wins, doc 04 §3.4) while every non-coalescible event is retained.
func TestHubTickCoalesce(t *testing.T) {
	h := NewHub(HubConfig{SubscriberBuffer: 8}, nil)
	ch, cancel := h.Subscribe([]string{"overview", "key"})
	defer cancel()

	// The pump immediately moves one event into the unbuffered channel send; publish a first
	// event to occupy it, then flood ticks while nothing drains.
	first := h.Publish(Event{Name: "key.status.changed"})
	time.Sleep(20 * time.Millisecond) // let the pump block on the channel send
	for i := 0; i < 50; i++ {
		h.Publish(Event{Name: "overview.tiles.tick", Payload: i})
	}
	lastTick := h.Publish(Event{Name: "overview.tiles.tick", Payload: "latest"})
	second := h.Publish(Event{Name: "key.status.changed"})

	got := map[ID]Event{}
	for i := 0; i < 3; i++ {
		e, ok := recv(t, ch, time.Second)
		if !ok {
			t.Fatal("channel closed")
		}
		got[e.ID] = e
	}
	if _, ok := got[first]; !ok {
		t.Error("first changed event missing")
	}
	if _, ok := got[second]; !ok {
		t.Error("second changed event missing")
	}
	e, ok := got[lastTick]
	if !ok {
		t.Fatal("latest tick missing (coalesce must keep the newest)")
	}
	if e.Payload != "latest" {
		t.Errorf("tick payload = %v, want latest", e.Payload)
	}
	select {
	case e := <-ch:
		t.Fatalf("unexpected extra event %v (intermediate ticks must coalesce away)", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestHubForcedDisconnect: overflowing a subscriber with non-coalescible events closes the
// channel (close-don't-drop) instead of silently losing any changed event.
func TestHubForcedDisconnect(t *testing.T) {
	h := NewHub(HubConfig{SubscriberBuffer: 4}, nil)
	ch, cancel := h.Subscribe([]string{"key"})
	defer cancel()
	time.Sleep(10 * time.Millisecond)
	// nothing drains ch; 1 event sits in the pump's send + 4 fill the queue + 1 overflows
	for i := 0; i < 10; i++ {
		h.Publish(Event{Name: "key.status.changed"})
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if h.Clients() != 0 {
					t.Fatalf("clients = %d after forced disconnect, want 0", h.Clients())
				}
				return // forced disconnect observed
			}
		case <-deadline:
			t.Fatal("subscriber was not force-disconnected on overflow")
		}
	}
}

// TestHubReplayExact: reconnecting with an in-ring id yields exactly the missed events in
// order; a scrolled-out id yields a reset gap for that topic.
func TestHubReplayExact(t *testing.T) {
	h := NewHub(HubConfig{RingSize: 8}, nil)
	var ids []ID
	for i := 0; i < 6; i++ {
		ids = append(ids, h.Publish(Event{Name: "key.status.changed", Payload: i}))
	}
	replay, gaps, ch, cancel := h.SubscribeFrom([]string{"key"}, ids[2])
	defer cancel()
	if len(gaps) != 0 {
		t.Fatalf("gaps = %v, want none", gaps)
	}
	if len(replay) != 3 {
		t.Fatalf("replay len = %d, want 3", len(replay))
	}
	for i, e := range replay {
		if e.ID != ids[3+i] {
			t.Fatalf("replay[%d] = %v, want %v", i, e.ID, ids[3+i])
		}
	}
	// Live events continue after the replay set.
	next := h.Publish(Event{Name: "key.status.changed"})
	e, _ := recv(t, ch, time.Second)
	if e.ID != next {
		t.Fatalf("live event id = %v, want %v", e.ID, next)
	}
}

func TestHubReplayGapReset(t *testing.T) {
	h := NewHub(HubConfig{RingSize: 4}, nil)
	old := h.Publish(Event{Name: "import.batch.progress"})
	for i := 0; i < 10; i++ { // scroll `old` out of the 4-slot ring
		h.Publish(Event{Name: "import.batch.progress"})
	}
	replay, gaps, _, cancel := h.SubscribeFrom([]string{"import", "key"}, old)
	defer cancel()
	if len(gaps) != 1 || gaps[0] != "import" {
		t.Fatalf("gaps = %v, want [import]", gaps)
	}
	if len(replay) != 0 {
		t.Fatalf("replay across a gap must be empty, got %d", len(replay))
	}
}

func TestEventTopicAndQoS(t *testing.T) {
	for _, name := range EventNames {
		e := Event{Name: name}
		if !ValidTopic(e.Topic()) {
			t.Errorf("event %q topic %q not in vocabulary", name, e.Topic())
		}
	}
	if !(Event{Name: "overview.tiles.tick"}).Coalescible() {
		t.Error("tick must be coalescible")
	}
	if (Event{Name: "key.status.changed"}).Coalescible() {
		t.Error("changed must not be coalescible")
	}
	if len(Topics) != 8 {
		t.Errorf("topics = %d, want 8", len(Topics))
	}
}
