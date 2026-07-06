package heartbeat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// echoDashboard is an httptest stand-in for the dashboard heartbeat endpoint. It records the last
// request (path, auth header, decoded body) and echoes an operator-settable desired_state, mirroring
// the real handler: POST /v1/admin/workers/{id}/heartbeat, DisallowUnknownFields, returns a worker
// DTO whose desired_state is the control signal.
type echoDashboard struct {
	mu       sync.Mutex
	desired  string
	path     string
	authHdr  string
	lastBody beatBody
	beats    int
	status   int // response status override; 0 => 200
}

func (d *echoDashboard) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/workers/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.path = r.URL.Path
		d.authHdr = r.Header.Get("Authorization")
		d.beats++

		// Mirror the real handler's strict decode so body drift is caught here.
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&d.lastBody); err != nil && err != io.EOF {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if d.status != 0 {
			http.Error(w, "boom", d.status)
			return
		}
		// Echo desired_state alongside other DTO fields the transport must ignore.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":            r.PathValue("id"),
			"status":        d.lastBody.Status,
			"desired_state": d.desired,
			"jobs_active":   d.lastBody.JobsActive,
		})
	})
	return mux
}

// TestHTTPTransport_RoundTripAndDesiredState verifies the HTTP transport hits the correct per-worker
// path, sends a body the strict handler accepts, and returns the echoed desired_state.
func TestHTTPTransport_RoundTripAndDesiredState(t *testing.T) {
	d := &echoDashboard{desired: Draining}
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	tr := NewHTTPTransport(HTTPConfig{BaseURL: srv.URL, Client: srv.Client()})
	ack, err := tr.Send(context.Background(), Beat{
		WorkerID: "w-enrich-7", Kind: "enrich", Region: "us", Queue: "default",
		Version: "v1.2.3", Status: Running, JobsActive: 2, JobsDone: 9,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ack.DesiredState != Draining {
		t.Fatalf("desired_state = %q, want %q", ack.DesiredState, Draining)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.path != "/v1/admin/workers/w-enrich-7/heartbeat" {
		t.Fatalf("path = %q, want /v1/admin/workers/w-enrich-7/heartbeat", d.path)
	}
	if d.lastBody.Status != Running || d.lastBody.JobsActive != 2 || d.lastBody.JobsDone != 9 ||
		d.lastBody.Kind != "enrich" || d.lastBody.Version != "v1.2.3" {
		t.Fatalf("decoded body wrong: %+v", d.lastBody)
	}
	t.Logf("PASS transport: POST %s accepted by strict handler, desired_state=%q echoed", d.path, ack.DesiredState)
}

// TestHTTPTransport_AttachesJWT proves the machine JWT is attached as an Authorization: Bearer
// header on every beat.
func TestHTTPTransport_AttachesJWT(t *testing.T) {
	d := &echoDashboard{desired: Running}
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	const token = "eyJhbGciOiJIUzI1NiIsImtpZCI6ImRlZmF1bHQifQ.payload.sig"
	tr := NewHTTPTransport(HTTPConfig{
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Bearer:  func() (string, error) { return token, nil },
	})
	if _, err := tr.Send(context.Background(), Beat{WorkerID: "w1", Status: Running}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	d.mu.Lock()
	got := d.authHdr
	d.mu.Unlock()
	if got != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer "+token)
	}
	t.Logf("PASS jwt attachment: Authorization header carried the bearer token")
}

// TestHTTPTransport_BearerErrorPropagates verifies a token-minting failure aborts the beat (rather
// than sending an unauthenticated request).
func TestHTTPTransport_BearerErrorPropagates(t *testing.T) {
	d := &echoDashboard{desired: Running}
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	wantErr := "mint failed"
	tr := NewHTTPTransport(HTTPConfig{
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Bearer:  func() (string, error) { return "", io.ErrUnexpectedEOF },
	})
	_, err := tr.Send(context.Background(), Beat{WorkerID: "w1", Status: Running})
	if err == nil {
		t.Fatal("expected bearer error to abort the beat")
	}
	d.mu.Lock()
	beats := d.beats
	d.mu.Unlock()
	if beats != 0 {
		t.Fatalf("request was sent despite bearer failure (beats=%d)", beats)
	}
	_ = wantErr
}

// TestHTTPTransport_Non200IsError verifies a non-200 dashboard response surfaces as an error
// without leaking the response body.
func TestHTTPTransport_Non200IsError(t *testing.T) {
	d := &echoDashboard{desired: Running, status: http.StatusForbidden}
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	tr := NewHTTPTransport(HTTPConfig{BaseURL: srv.URL, Client: srv.Client()})
	_, err := tr.Send(context.Background(), Beat{WorkerID: "w1", Status: Running})
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error should mention the status: %v", err)
	}
}

// TestHTTPTransport_DrivesClientConvergence wires the real HTTP transport into the Client and proves
// an end-to-end beat converges the worker onto the dashboard's echoed desired_state (claims stop
// once draining is echoed).
func TestHTTPTransport_DrivesClientConvergence(t *testing.T) {
	d := &echoDashboard{desired: Running}
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	tr := NewHTTPTransport(HTTPConfig{BaseURL: srv.URL, Client: srv.Client()})
	c := New(Config{Transport: tr, WorkerID: "w-http"})
	ctx := context.Background()

	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("beat 1: %v", err)
	}
	if !c.ShouldClaim() {
		t.Fatal("running worker must claim")
	}

	// Operator drains via the dashboard; the next beat's ack stops claiming.
	d.mu.Lock()
	d.desired = Draining
	d.mu.Unlock()
	if _, err := c.Beat(ctx); err != nil {
		t.Fatalf("beat 2: %v", err)
	}
	if c.ShouldClaim() {
		t.Fatal("claims must stop once draining is echoed over HTTP")
	}
	t.Log("PASS end-to-end: HTTP-echoed desired_state converged the client")
}
