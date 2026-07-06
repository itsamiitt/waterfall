package realtime

import (
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/metrics"
)

// HubConfig tunes the per-instance fan-out. Zero values fall back to the doc 04 §3.5 defaults.
type HubConfig struct {
	RingSize         int              // per-topic replay ring (default 256, doc 04 §3.5)
	SubscriberBuffer int              // bounded per-subscriber send buffer (default 256)
	Now              func() time.Time // injectable clock (tests)
}

func (c HubConfig) withDefaults() HubConfig {
	if c.RingSize <= 0 {
		c.RingSize = 256
	}
	if c.SubscriberBuffer <= 0 {
		c.SubscriberBuffer = 256
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Hub is the per-instance fan-out (ADR-0019): Publish stamps a monotonic `<epochms>-<seq>` id,
// appends to the topic's 256-event replay ring, and pushes to every subscriber of that topic.
// QoS: coalescible ticks are latest-wins per topic when a subscriber lags; a non-coalescible
// event that cannot fit a subscriber's bounded buffer force-disconnects that subscriber
// (close-don't-drop, doc 04 §3.5). Hub implements Source.
type Hub struct {
	cfg HubConfig

	mu     sync.Mutex
	lastID ID
	rings  map[string]*ring
	subs   map[*subscriber]struct{}

	clientsG *metrics.Gauge
	eventsC  *metrics.Counter
	droppedC *metrics.Counter
}

var _ Source = (*Hub)(nil)

// NewHub builds a Hub. reg may be nil (private registry).
func NewHub(cfg HubConfig, reg *metrics.Registry) *Hub {
	if reg == nil {
		reg = metrics.New()
	}
	return &Hub{
		cfg:   cfg.withDefaults(),
		rings: map[string]*ring{},
		subs:  map[*subscriber]struct{}{},
		clientsG: reg.Gauge("dash_sse_clients",
			"current SSE subscribers on this instance"),
		eventsC: reg.Counter("dash_sse_events_total",
			"events published to the SSE hub by topic", "topic"),
		droppedC: reg.Counter("dash_sse_dropped_total",
			"subscribers force-disconnected because a non-coalescible event overflowed their buffer (doc 04 §3.5 close-don't-drop)"),
	}
}

// Publish stamps e with a monotonic id + emission timestamp, appends it to the topic ring, and
// fans it out. Events with names outside the vocabulary shape (no dot) are still delivered by
// their literal first segment — the poller and aggregator only emit the closed vocabulary.
func (h *Hub) Publish(e Event) ID {
	h.mu.Lock()
	now := h.cfg.Now().UTC()
	if e.TS.IsZero() {
		e.TS = now
	}
	ms := now.UnixMilli()
	if ms < h.lastID.Ms {
		ms = h.lastID.Ms // clock regression: clamp so ids stay monotonic
	}
	h.lastID = ID{Ms: ms, Seq: h.lastID.Seq + 1}
	e.ID = h.lastID

	topic := e.Topic()
	r := h.rings[topic]
	if r == nil {
		r = newRing(h.cfg.RingSize)
		h.rings[topic] = r
	}
	r.append(e)

	var dead []*subscriber
	for s := range h.subs {
		if !s.topics[topic] {
			continue
		}
		if !s.push(e) {
			dead = append(dead, s)
		}
	}
	for _, s := range dead {
		delete(h.subs, s)
		h.droppedC.Inc()
	}
	if len(dead) > 0 {
		h.clientsG.Set(float64(len(h.subs)))
	}
	h.mu.Unlock()
	h.eventsC.Inc(topic)
	return e.ID
}

// Subscribe implements Source: a live subscription with no replay.
func (h *Hub) Subscribe(topics []string) (<-chan Event, func()) {
	_, _, ch, cancel := h.SubscribeFrom(topics, ID{})
	return ch, cancel
}

// SubscribeFrom atomically registers a subscriber and computes the Last-Event-ID replay set
// (doc 04 §3.5): every buffered event after `after` across the subscribed topics, in id order.
// gaps lists topics whose ring evicted events newer than `after` — the caller must emit an
// explicit `reset` for them. A zero `after` means a fresh stream (no replay, no gaps). Events
// published after this call flow on ch; the replay slice and ch never overlap or miss.
func (h *Hub) SubscribeFrom(topics []string, after ID) (replay []Event, gaps []string, ch <-chan Event, cancel func()) {
	s := &subscriber{
		topics: map[string]bool{},
		out:    make(chan Event),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
		ticks:  map[string]Event{},
		max:    h.cfg.SubscriberBuffer,
	}
	for _, t := range topics {
		s.topics[t] = true
	}

	h.mu.Lock()
	if !after.IsZero() {
		for _, t := range topics {
			r := h.rings[t]
			if r == nil {
				continue
			}
			if r.evicted && after.Less(r.lastEvicted) {
				gaps = append(gaps, t)
				continue
			}
			replay = append(replay, r.after(after)...)
		}
		sortEvents(replay)
	}
	h.subs[s] = struct{}{}
	h.clientsG.Set(float64(len(h.subs)))
	h.mu.Unlock()

	go s.pump()

	cancelFn := func() {
		h.mu.Lock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			h.clientsG.Set(float64(len(h.subs)))
		}
		h.mu.Unlock()
		s.close()
	}
	return replay, gaps, s.out, cancelFn
}

// noteDropped counts a forced disconnect that happened outside the hub's own overflow path
// (the SSE per-write deadline close) into dash_sse_dropped_total.
func (h *Hub) noteDropped() { h.droppedC.Inc() }

// Clients reports the current subscriber count (self_monitor sse_clients row, doc 10 §4).
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// NextID exposes id stamping for control events (reset) that bypass the rings.
func (h *Hub) NextID() ID {
	h.mu.Lock()
	defer h.mu.Unlock()
	ms := h.cfg.Now().UTC().UnixMilli()
	if ms < h.lastID.Ms {
		ms = h.lastID.Ms
	}
	h.lastID = ID{Ms: ms, Seq: h.lastID.Seq + 1}
	return h.lastID
}

// sortEvents orders events by id (insertion sort — replay sets are small and mostly ordered).
func sortEvents(evs []Event) {
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j].ID.Less(evs[j-1].ID); j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}
}

// --- replay ring ---

// ring is a fixed-size per-topic event buffer (doc 04 §3.5: 256 events per topic). It records
// the newest evicted id so replay can distinguish "older than everything but nothing was lost"
// from a genuine gap.
type ring struct {
	buf         []Event
	start, n    int
	evicted     bool
	lastEvicted ID
}

func newRing(size int) *ring { return &ring{buf: make([]Event, size)} }

func (r *ring) append(e Event) {
	if r.n == len(r.buf) {
		r.lastEvicted = r.buf[r.start].ID
		r.evicted = true
		r.buf[r.start] = e
		r.start = (r.start + 1) % len(r.buf)
		return
	}
	r.buf[(r.start+r.n)%len(r.buf)] = e
	r.n++
}

// after returns the buffered events with id > after, oldest first.
func (r *ring) after(after ID) []Event {
	var out []Event
	for i := 0; i < r.n; i++ {
		e := r.buf[(r.start+i)%len(r.buf)]
		if after.Less(e.ID) {
			out = append(out, e)
		}
	}
	return out
}

// --- subscriber ---

// subscriber is one bounded fan-out endpoint. Non-coalescible events queue FIFO (overflow =>
// forced close); coalescible ticks live in a latest-wins per-topic slot. The pump goroutine
// delivers the pending event with the smallest id, so wire ids stay monotonic per stream.
type subscriber struct {
	topics map[string]bool
	out    chan Event
	wake   chan struct{}
	done   chan struct{} // closed exactly once on close(); unblocks a pump stuck in send
	once   sync.Once
	max    int

	mu     sync.Mutex
	queue  []Event          // non-coalescible, FIFO, bounded by max
	ticks  map[string]Event // topic -> latest coalescible event
	closed bool
}

// push enqueues e; the false return means the subscriber overflowed on a non-coalescible event
// and must be force-disconnected (the hub removes it; pump drains then closes out).
func (s *subscriber) push(e Event) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return true // already closing; nothing to do, not a new overflow
	}
	if e.Coalescible() {
		s.ticks[e.Topic()] = e // latest wins per topic (doc 04 §3.4)
	} else {
		if len(s.queue) >= s.max {
			s.closed = true // close-don't-drop (doc 04 §3.5)
			s.mu.Unlock()
			s.once.Do(func() { close(s.done) })
			s.signal()
			return false
		}
		s.queue = append(s.queue, e)
	}
	s.mu.Unlock()
	s.signal()
	return true
}

func (s *subscriber) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
	s.signal()
}

// next pops the smallest-id pending event. ok=false when nothing is pending.
func (s *subscriber) next() (Event, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := Event{}
	haveBest := false
	fromQueue := false
	if len(s.queue) > 0 {
		best, haveBest, fromQueue = s.queue[0], true, true
	}
	bestTopic := ""
	for t, e := range s.ticks {
		if !haveBest || e.ID.Less(best.ID) {
			best, haveBest, fromQueue, bestTopic = e, true, false, t
		}
	}
	if !haveBest {
		return Event{}, false, s.closed
	}
	if fromQueue {
		s.queue = s.queue[1:]
	} else {
		delete(s.ticks, bestTopic)
	}
	return best, true, s.closed
}

// pump delivers pending events to out in id order; when the subscriber is closed (cancel or
// forced disconnect) it stops and closes out — the SSE handler observes the closed channel as
// the disconnect signal. A forced close discards undelivered events by design: the client
// replays them from the ring on reconnect (or receives reset), never a silent loss.
func (s *subscriber) pump() {
	defer close(s.out)
	for {
		e, ok, closed := s.next()
		if closed {
			return
		}
		if !ok {
			select {
			case <-s.wake:
			case <-s.done:
				return
			}
			continue
		}
		select {
		case s.out <- e:
		case <-s.done:
			return
		}
	}
}
