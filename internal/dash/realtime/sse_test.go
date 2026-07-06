package realtime

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/tenant"
)

type stubAuth struct {
	p   tenant.Principal
	err error
}

func (a stubAuth) Authenticate(*http.Request) (tenant.Principal, error) { return a.p, a.err }

func operatorAuth() stubAuth {
	return stubAuth{p: tenant.Principal{TenantID: "platform", UserID: "u1", Scopes: []string{"role:operator"}}}
}

func newStreamServer(t *testing.T, hub *Hub, cfg StreamConfig, auth Authenticator) (*httptest.Server, *Streams) {
	t.Helper()
	mux := http.NewServeMux()
	s := Routes(mux, Deps{Hub: hub, Auth: auth, Config: cfg})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, s
}

// frame is one parsed SSE frame (event/id/data) or a comment.
type frame struct {
	comment bool
	event   string
	id      string
	data    string
}

// readFrames consumes the stream until n non-comment frames (or wantComments comments) arrive
// or the timeout lapses, returning everything parsed.
func readFrames(t *testing.T, body io.Reader, n int, timeout time.Duration) []frame {
	t.Helper()
	type result struct {
		frames []frame
	}
	done := make(chan result, 1)
	go func() {
		var frames []frame
		var cur frame
		flush := func() {
			if cur.comment || cur.event != "" || cur.data != "" {
				frames = append(frames, cur)
			}
			cur = frame{}
		}
		count := 0
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.event != "" || cur.data != "" || cur.comment {
					if !cur.comment && cur.event != "" {
						count++
					}
					flush()
					if count >= n {
						done <- result{frames}
						return
					}
				}
			case strings.HasPrefix(line, ":"):
				frames = append(frames, frame{comment: true, data: strings.TrimSpace(line[1:])})
			case strings.HasPrefix(line, "event: "):
				cur.event = line[len("event: "):]
			case strings.HasPrefix(line, "id: "):
				cur.id = line[len("id: "):]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[len("data: "):]
			case strings.HasPrefix(line, "retry: "):
				// reconnect hint; not recorded
			}
		}
		done <- result{frames}
	}()
	select {
	case r := <-done:
		return r.frames
	case <-time.After(timeout):
		return nil // caller asserts on whatever it needs; timeout => empty
	}
}

func openStream(t *testing.T, url string, lastEventID string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	return resp, cancel
}

// TestSSEHeartbeat (P7 acceptance #4): an idle stream carries a `: hb` comment every
// heartbeat interval, observed by a recording client (interval shortened via config).
func TestSSEHeartbeat(t *testing.T) {
	hub := NewHub(HubConfig{}, nil)
	srv, _ := newStreamServer(t, hub, StreamConfig{HeartbeatInterval: 50 * time.Millisecond}, operatorAuth())

	resp, cancel := openStream(t, srv.URL+"/v1/admin/streams?topics=overview", "")
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("X-Accel-Buffering: no missing (proxy buffering guard)")
	}

	deadline := time.After(2 * time.Second)
	got := 0
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for got < 4 { // 1 initial + >=3 interval heartbeats on an idle stream
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed early")
			}
			if strings.HasPrefix(line, ": hb") {
				got++
			}
		case <-deadline:
			t.Fatalf("saw %d heartbeats in 2s, want >= 4 at 50ms interval", got)
		}
	}
}

// TestSSEReplayInOrder (P7 acceptance #2, happy path): a reconnect with an in-ring
// Last-Event-ID receives exactly the missed events, in order.
func TestSSEReplayInOrder(t *testing.T) {
	hub := NewHub(HubConfig{}, nil)
	srv, _ := newStreamServer(t, hub, StreamConfig{HeartbeatInterval: time.Hour}, operatorAuth())

	var ids []ID
	for i := 0; i < 6; i++ {
		ids = append(ids, hub.Publish(Event{
			Name:    "key.status.changed",
			Scope:   map[string]string{"key_id": "k"},
			Payload: map[string]any{"i": i},
		}))
	}
	resp, cancel := openStream(t, srv.URL+"/v1/admin/streams?topics=key", ids[2].String())
	defer cancel()
	defer resp.Body.Close()

	frames := readFrames(t, resp.Body, 3, 2*time.Second)
	var events []frame
	for _, f := range frames {
		if !f.comment && f.event != "" {
			events = append(events, f)
		}
	}
	if len(events) != 3 {
		t.Fatalf("replayed events = %d, want exactly 3 (the missed ones)", len(events))
	}
	for i, f := range events {
		if f.event != "key.status.changed" {
			t.Errorf("event %d name = %q", i, f.event)
		}
		if f.id != ids[3+i].String() {
			t.Errorf("event %d id = %s, want %s (exact order)", i, f.id, ids[3+i])
		}
		var env struct {
			V       int               `json:"v"`
			TS      string            `json:"ts"`
			Scope   map[string]string `json:"scope"`
			Payload map[string]any    `json:"payload"`
		}
		if err := json.Unmarshal([]byte(f.data), &env); err != nil {
			t.Fatalf("event %d data not an envelope: %v", i, err)
		}
		if env.V != 1 || env.TS == "" || env.Scope["key_id"] != "k" {
			t.Errorf("event %d envelope = %+v, want v=1 + ts + scope", i, env)
		}
	}
}

// TestSSEResetOnScrolledOutID (P7 acceptance #2, overflow path): an id that scrolled out of
// the ring yields an explicit `event: reset` naming the gapped topics.
func TestSSEResetOnScrolledOutID(t *testing.T) {
	hub := NewHub(HubConfig{RingSize: 4}, nil)
	srv, _ := newStreamServer(t, hub, StreamConfig{HeartbeatInterval: time.Hour}, operatorAuth())

	old := hub.Publish(Event{Name: "import.batch.progress"})
	for i := 0; i < 10; i++ {
		hub.Publish(Event{Name: "import.batch.progress"})
	}
	resp, cancel := openStream(t, srv.URL+"/v1/admin/streams?topics=import", old.String())
	defer cancel()
	defer resp.Body.Close()

	frames := readFrames(t, resp.Body, 1, 2*time.Second)
	var reset *frame
	for i := range frames {
		if frames[i].event == "reset" {
			reset = &frames[i]
			break
		}
	}
	if reset == nil {
		t.Fatalf("no reset event observed; frames = %+v", frames)
	}
	var body struct {
		V      int      `json:"v"`
		Topics []string `json:"topics"`
	}
	if err := json.Unmarshal([]byte(reset.data), &body); err != nil {
		t.Fatalf("reset data: %v", err)
	}
	if body.V != 1 || len(body.Topics) != 1 || body.Topics[0] != "import" {
		t.Fatalf("reset body = %+v, want topics [import]", body)
	}
	if reset.id == "" {
		t.Fatal("reset must carry an id so Last-Event-ID advances")
	}
}

// TestSSESaturated: the SSE_MAX_CONNS cap rejects the connection over it with
// 503 {"error":{"code":"sse_saturated"}} + Retry-After (doc 04 §3.5).
func TestSSESaturated(t *testing.T) {
	hub := NewHub(HubConfig{}, nil)
	srv, streams := newStreamServer(t, hub, StreamConfig{MaxConns: 1, HeartbeatInterval: time.Hour}, operatorAuth())

	resp1, cancel1 := openStream(t, srv.URL+"/v1/admin/streams?topics=overview", "")
	defer cancel1()
	defer resp1.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for streams.ActiveConns() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	resp2, err := http.Get(srv.URL + "/v1/admin/streams?topics=overview")
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp2.StatusCode)
	}
	if resp2.Header.Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
	var eb errorBody
	if err := json.NewDecoder(resp2.Body).Decode(&eb); err != nil || eb.Error.Code != codeSSESaturated {
		t.Fatalf("error code = %q (%v), want sse_saturated", eb.Error.Code, err)
	}
}

// TestSSETopicValidation: unknown topic -> 400 invalid_filter; a topic outside the caller's
// RBAC -> 403 forbidden (no silent filtering); empty topics -> 400 (doc 04 §3.1/§3.2).
func TestSSETopicValidation(t *testing.T) {
	hub := NewHub(HubConfig{}, nil)
	tenantUser := stubAuth{p: tenant.Principal{TenantID: "t1", UserID: "u2", Scopes: []string{"role:tenant_user"}}}
	srv, _ := newStreamServer(t, hub, StreamConfig{}, tenantUser)

	cases := []struct {
		query  string
		status int
		code   string
	}{
		{"topics=nonsense", 400, codeInvalidFilter},
		{"topics=", 400, codeInvalidFilter},
		{"topics=queue", 403, codeForbidden}, // queue is operator-only (doc 04 §3.2)
		{"topics=key", 403, codeForbidden},   // key is O/TA
		{"topics=overview,worker", 403, codeForbidden},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + "/v1/admin/streams?" + tc.query)
		if err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		var eb errorBody
		_ = json.NewDecoder(resp.Body).Decode(&eb)
		resp.Body.Close()
		if resp.StatusCode != tc.status || eb.Error.Code != tc.code {
			t.Errorf("%s = %d %q, want %d %q", tc.query, resp.StatusCode, eb.Error.Code, tc.status, tc.code)
		}
	}

	// A tenant_user CAN stream the TU+ topics.
	resp, cancel := openStream(t, srv.URL+"/v1/admin/streams?topics=overview,provider,alert", "")
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("TU+ topics status = %d, want 200", resp.StatusCode)
	}
}
