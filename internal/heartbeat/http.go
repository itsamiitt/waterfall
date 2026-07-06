package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPConfig configures the HTTP heartbeat Transport (doc 12 §P5). It is opt-in wiring for enrichd:
// the client posts to the dashboard's RBAC-gated POST /v1/admin/workers/{id}/heartbeat and reads
// back the echoed desired_state (the only control signal).
type HTTPConfig struct {
	// BaseURL is the dashboard's base URL (scheme://host[:port][/prefix]). The transport appends
	// the per-worker path /v1/admin/workers/{id}/heartbeat, taking {id} from each Beat's WorkerID —
	// so one BaseURL serves any worker id and the id never rides in the body.
	BaseURL string
	// Client is the HTTP client used for the round-trip. nil falls back to a 5s-timeout default.
	// Callers that need TLS pinning / egress control pass their own.
	Client *http.Client
	// Bearer returns the Authorization bearer token (a machine JWT with role:operator + admin:write)
	// to attach to each beat. It is a func, not a static string, so a short-lived minted token can
	// be refreshed per beat. A nil Bearer (or one returning "") sends no Authorization header.
	Bearer func() (string, error)
}

// HTTPTransport is the production heartbeat.Transport: it serializes a Beat to the dashboard's
// heartbeat endpoint with a bearer JWT and decodes the echoed desired_state. It never logs the
// token or the response body.
type HTTPTransport struct {
	base   string
	client *http.Client
	bearer func() (string, error)
}

// NewHTTPTransport builds an HTTPTransport from cfg, applying the default client.
func NewHTTPTransport(cfg HTTPConfig) *HTTPTransport {
	c := cfg.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	return &HTTPTransport{
		base:   strings.TrimRight(cfg.BaseURL, "/"),
		client: c,
		bearer: cfg.Bearer,
	}
}

var _ Transport = (*HTTPTransport)(nil)

// beatBody is the POST body. Its fields MUST match internal/dash/workers.beatReq exactly — that
// handler decodes with DisallowUnknownFields, so an extra/renamed field would 400. The worker id
// is NOT in the body (it is the {id} path segment).
type beatBody struct {
	Kind       string  `json:"kind"`
	Region     string  `json:"region"`
	Queue      string  `json:"queue"`
	Version    string  `json:"version"`
	Status     string  `json:"status"`
	CPUPct     float64 `json:"cpu_pct"`
	MemMB      float64 `json:"mem_mb"`
	JobsActive int     `json:"jobs_active"`
	JobsDone   int64   `json:"jobs_done"`
}

// ackBody is the slice of the worker DTO the convergence loop needs: the echoed desired_state.
type ackBody struct {
	DesiredState string `json:"desired_state"`
}

// endpoint builds the per-worker heartbeat URL. The id is path-escaped so an id with reserved
// characters cannot alter the route.
func (t *HTTPTransport) endpoint(workerID string) string {
	return t.base + "/v1/admin/workers/" + url.PathEscape(workerID) + "/heartbeat"
}

// Send posts one beat and returns the dashboard's echoed desired_state.
func (t *HTTPTransport) Send(ctx context.Context, b Beat) (Ack, error) {
	body, err := json.Marshal(beatBody{
		Kind: b.Kind, Region: b.Region, Queue: b.Queue, Version: b.Version,
		Status: b.Status, CPUPct: b.CPUPct, MemMB: b.MemMB,
		JobsActive: b.JobsActive, JobsDone: b.JobsDone,
	})
	if err != nil {
		return Ack{}, fmt.Errorf("heartbeat: encode beat: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(b.WorkerID), bytes.NewReader(body))
	if err != nil {
		return Ack{}, fmt.Errorf("heartbeat: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if t.bearer != nil {
		tok, berr := t.bearer()
		if berr != nil {
			return Ack{}, fmt.Errorf("heartbeat: acquire bearer token: %w", berr)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return Ack{}, err
	}
	defer resp.Body.Close()
	// Bounded read so a misbehaving peer cannot exhaust memory; drain enables connection reuse.
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// Status only — never surface the token or response body (no secrets in logs/errors).
		return Ack{}, fmt.Errorf("heartbeat: dashboard returned %s", resp.Status)
	}
	var ab ackBody
	if err := json.Unmarshal(data, &ab); err != nil {
		return Ack{}, fmt.Errorf("heartbeat: decode ack: %w", err)
	}
	return Ack{DesiredState: ab.DesiredState}, nil
}
