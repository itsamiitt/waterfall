package realtime

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

const streamsPath = "/v1/admin/streams"

// Error codes (doc 04 §1.6 registry subset) — same envelope shape as the shared httpx writer.
const (
	codeUnauthorized  = "unauthorized"
	codeForbidden     = "forbidden"
	codeInvalidFilter = "invalid_filter"
	codeSSESaturated  = "sse_saturated"
)

// Authenticator resolves a request into a verified Principal (satisfied by
// httpx.CtxAuthenticator behind the shared FeatureChain; this package never imports httpx).
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// StreamConfig tunes the SSE endpoint. Zero values fall back to the doc 04 §3.5 / doc 11 §2
// deployment defaults (SSE_MAX_CONNS=500, SSE_WRITE_DEADLINE=10s, 15s heartbeat, retry: 5000).
type StreamConfig struct {
	MaxConns          int           // per-instance connection cap -> 503 sse_saturated over it
	WriteDeadline     time.Duration // per-write deadline via http.ResponseController
	HeartbeatInterval time.Duration // `: hb` comment cadence
	RetryMillis       int           // retry: reconnect hint
}

func (c StreamConfig) withDefaults() StreamConfig {
	if c.MaxConns <= 0 {
		c.MaxConns = 500
	}
	if c.WriteDeadline <= 0 {
		c.WriteDeadline = 10 * time.Second
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 15 * time.Second
	}
	if c.RetryMillis <= 0 {
		c.RetryMillis = 5000
	}
	return c
}

// Deps bundles the streams endpoint collaborators.
type Deps struct {
	Hub    *Hub
	Auth   Authenticator
	Config StreamConfig
	Logger *slog.Logger
}

// Streams is the mounted SSE surface; it tracks the per-instance connection count for the cap
// and the self_monitor sse_clients row.
type Streams struct {
	hub    *Hub
	auth   Authenticator
	cfg    StreamConfig
	log    *slog.Logger
	active atomic.Int64
}

// Routes mounts GET /v1/admin/streams (doc 04 §2.13) on mux and returns the Streams for
// wiring (self-monitor loop reads ActiveConns). The endpoint is a GET and is therefore NOT
// CSRF-gated (EventSource cannot send custom headers); authentication comes from the shared
// FeatureChain session/JWT resolution.
func Routes(mux *http.ServeMux, d Deps) *Streams {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Streams{hub: d.Hub, auth: d.Auth, cfg: d.Config.withDefaults(), log: log}
	mux.HandleFunc("GET "+streamsPath, s.handle)
	return s
}

// ActiveConns reports the currently-open stream count (cap accounting; self_monitor row).
func (s *Streams) ActiveConns() int64 { return s.active.Load() }

// topicAllowed encodes the doc 04 §3.2 per-topic minimum RBAC verbatim:
// overview/provider/alert = TU+ (any authenticated role); key/import = operator or
// tenant_admin (BYO); queue/worker = operator only; approval = TA+.
func topicAllowed(role, topic string) bool {
	switch topic {
	case TopicOverview, TopicProvider, TopicAlert:
		return role == rbac.RoleOperator || role == rbac.RoleTenantAdmin || role == rbac.RoleTenantUser
	case TopicKey, TopicImport:
		return role == rbac.RoleOperator || role == rbac.RoleTenantAdmin
	case TopicQueue, TopicWorker:
		return role == rbac.RoleOperator
	case TopicApproval:
		return role == rbac.RoleOperator || role == rbac.RoleTenantAdmin
	}
	return false
}

func (s *Streams) handle(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth.Authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
		return
	}
	role := db.RoleFromPrincipal(p)
	if !rbac.Can(role, rbac.OverviewRead).Allowed() {
		writeError(w, http.StatusForbidden, codeForbidden, "role does not permit streaming")
		return
	}

	topics, ok := parseTopics(w, r, role)
	if !ok {
		return
	}

	// Per-instance connection cap (doc 04 §3.5): enforced, not just alerted.
	if n := s.active.Add(1); n > int64(s.cfg.MaxConns) {
		s.active.Add(-1)
		w.Header().Set("Retry-After", "15")
		writeError(w, http.StatusServiceUnavailable, codeSSESaturated, "sse connection cap reached on this instance")
		return
	}
	defer s.active.Add(-1)

	// Last-Event-ID: header wins over the ?last_event_id= query mirror (doc 04 §3.1).
	after := ID{}
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = ParseID(v)
	} else if v := r.URL.Query().Get("last_event_id"); v != "" {
		after, _ = ParseID(v)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	cw := &sseWriter{w: w, rc: rc, deadline: s.cfg.WriteDeadline}
	if err := cw.raw("retry: " + strconv.Itoa(s.cfg.RetryMillis) + "\n\n: hb\n\n"); err != nil {
		return
	}

	replay, gaps, ch, cancel := s.hub.SubscribeFrom(topics, after)
	defer cancel()

	// Ring overflow: explicit reset naming the gapped topics; the client refetches their
	// snapshots. The reset carries a fresh id so the client's Last-Event-ID advances.
	if len(gaps) > 0 {
		if err := cw.reset(s.hub.NextID(), gaps); err != nil {
			return
		}
	}
	lastSent := ID{}
	for _, e := range replay {
		if err := cw.event(e); err != nil {
			s.noteWriteFailure(err)
			return
		}
		lastSent = e.ID
	}

	hb := time.NewTicker(s.cfg.HeartbeatInterval)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			if err := cw.raw(": hb\n\n"); err != nil {
				s.noteWriteFailure(err)
				return
			}
		case e, open := <-ch:
			if !open {
				// Forced disconnect: this subscriber overflowed on a non-coalescible event
				// (close-don't-drop). The client reconnects with Last-Event-ID and replays.
				return
			}
			if !lastSent.Less(e.ID) {
				continue // already replayed
			}
			if err := cw.event(e); err != nil {
				s.noteWriteFailure(err)
				return
			}
			lastSent = e.ID
		}
	}
}

// noteWriteFailure counts a slow/stalled client closed by the per-write deadline as a forced
// disconnect (doc 10: the delivery metric counts forced disconnects; there is no silent drop).
func (s *Streams) noteWriteFailure(err error) {
	s.hub.noteDropped()
	s.log.Info("sse stream closed on write", "err", err)
}

// parseTopics validates the csv topic set: unknown topic -> 400 invalid_filter; a topic
// outside the caller's RBAC -> 403 forbidden (no silent filtering, doc 04 §3.1).
func parseTopics(w http.ResponseWriter, r *http.Request, role string) ([]string, bool) {
	raw := r.URL.Query().Get("topics")
	if raw == "" {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "topics query parameter is required")
		return nil, false
	}
	seen := map[string]bool{}
	var topics []string
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		if !ValidTopic(t) {
			writeError(w, http.StatusBadRequest, codeInvalidFilter, "unknown SSE topic: "+t)
			return nil, false
		}
		if !topicAllowed(role, t) {
			writeError(w, http.StatusForbidden, codeForbidden, "topic outside caller's role: "+t)
			return nil, false
		}
		seen[t] = true
		topics = append(topics, t)
	}
	if len(topics) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "topics query parameter is required")
		return nil, false
	}
	return topics, true
}

// --- deadline-bounded SSE writer ---

// sseWriter serializes SSE frames with a per-write deadline (doc 04 §3.5: SSE_WRITE_DEADLINE
// via http.ResponseController; a missed deadline closes the connection so a stalled client
// costs one bounded goroutine for at most one deadline).
type sseWriter struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	deadline time.Duration
}

func (c *sseWriter) raw(s string) error {
	_ = c.rc.SetWriteDeadline(time.Now().Add(c.deadline))
	if _, err := c.w.Write([]byte(s)); err != nil {
		return err
	}
	return c.rc.Flush()
}

func (c *sseWriter) event(e Event) error {
	data, err := json.Marshal(envelopeFor(e))
	if err != nil {
		return err
	}
	var b strings.Builder
	b.Grow(len(data) + len(e.Name) + 48)
	b.WriteString("event: ")
	b.WriteString(e.Name)
	b.WriteString("\nid: ")
	b.WriteString(e.ID.String())
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return c.raw(b.String())
}

func (c *sseWriter) reset(id ID, topics []string) error {
	body, err := json.Marshal(map[string]any{"v": 1, "topics": topics})
	if err != nil {
		return err
	}
	return c.raw("event: reset\nid: " + id.String() + "\ndata: " + string(body) + "\n\n")
}

// --- uniform error envelope (identical shape to httpx.writeError; no import to avoid cycle) ---

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}
