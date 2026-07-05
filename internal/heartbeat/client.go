// Package heartbeat is the tiny enrichd-side client for the dashboard's worker control channel
// (doc 06 §4, doc 12 §P5): every 10s it upserts the worker's row and CONVERGES on the
// desired_state the response echoes — the heartbeat is the ONLY control channel (no SSH, no exec).
//
// Convergence semantics (doc 06 §4/§5):
//   - running  → claim normally.
//   - paused   → stop claiming; keep the process up; finish in-flight Enrichment Jobs.
//   - draining → stop claiming (skip the FOR UPDATE SKIP LOCKED poll) AND finish in-flight jobs
//     (they hold leased Provider Keys + reserved credits) BEFORE exit — drain ≠ stop.
//   - stopped  → stop; in-flight work is abandoned to the visibility-timeout reclaim path.
//
// The clock is injectable so the P5 drain-convergence gate is deterministic. The client is
// concurrency-safe: the beat loop and the worker goroutines that report job completions share one
// mutex. This package provides the client + its unit test; enrichd wiring is orchestrator/OI.
package heartbeat

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Desired-state / status vocabulary (mirrors internal/dash/workers).
const (
	Running  = "running"
	Draining = "draining"
	Paused   = "paused"
	Stopped  = "stopped"
)

// Beat is one heartbeat payload the transport sends to the dashboard.
type Beat struct {
	WorkerID   string
	Kind       string
	Region     string
	Queue      string
	Version    string
	Status     string
	CPUPct     float64
	MemMB      float64
	JobsActive int
	JobsDone   int64
}

// Ack is the dashboard's heartbeat response — it echoes the desired_state (the control signal).
type Ack struct {
	DesiredState string
}

// Transport sends a beat and returns the echoed Ack. It abstracts the HTTP round-trip (or, in
// tests, a fake dashboard) so the convergence logic is exercised without a live server.
type Transport interface {
	Send(ctx context.Context, b Beat) (Ack, error)
}

// Client tracks a worker's convergence toward the dashboard's desired_state.
type Client struct {
	transport Transport
	now       func() time.Time
	log       *slog.Logger

	mu       sync.Mutex
	workerID string
	kind     string
	region   string
	queue    string
	version  string

	desired    string // last echoed desired_state
	jobsActive int
	jobsDone   int64
	stopped    bool
}

// Config wires a Client. Now defaults to the wall clock.
type Config struct {
	Transport Transport
	WorkerID  string
	Kind      string
	Region    string
	Queue     string
	Version   string
	Now       func() time.Time
	Logger    *slog.Logger
}

// New builds a Client. It starts in the running desired-state (a fresh worker claims until told
// otherwise).
func New(cfg Config) *Client {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Client{
		transport: cfg.Transport,
		now:       cfg.Now,
		log:       cfg.Logger,
		workerID:  cfg.WorkerID,
		kind:      cfg.Kind,
		region:    cfg.Region,
		queue:     cfg.Queue,
		version:   cfg.Version,
		desired:   Running,
	}
}

// SetJobsActive sets the in-flight Enrichment Job count (the worker updates this as it claims).
func (c *Client) SetJobsActive(n int) {
	c.mu.Lock()
	c.jobsActive = n
	c.mu.Unlock()
}

// FinishJob records one in-flight Enrichment Job completing (decrements jobs_active, increments
// jobs_done). During a drain, this is what eventually lets the worker converge to stopped.
func (c *Client) FinishJob() {
	c.mu.Lock()
	if c.jobsActive > 0 {
		c.jobsActive--
	}
	c.jobsDone++
	c.mu.Unlock()
}

// ShouldClaim reports whether the worker may claim new work. Only the running desired-state
// permits claiming; paused/draining/stopped stop it immediately (drain = skip the poll).
func (c *Client) ShouldClaim() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.desired == Running && !c.stopped
}

// DesiredState returns the last echoed desired_state.
func (c *Client) DesiredState() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.desired
}

// Done reports whether the worker has converged to a terminal (stopped) state and may exit.
func (c *Client) Done() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// reportedStatus computes the status to report given the desired_state and in-flight count. A
// drain reports draining while jobs remain, then stopped once jobs_active reaches zero (marking
// the client Done). Caller holds the lock.
func (c *Client) reportedStatus() string {
	if c.stopped {
		return Stopped
	}
	switch c.desired {
	case Paused:
		return Paused
	case Draining:
		if c.jobsActive > 0 {
			return Draining
		}
		c.stopped = true
		return Stopped
	case Stopped:
		c.stopped = true
		return Stopped
	default:
		return Running
	}
}

// Beat sends one heartbeat and converges on the echoed desired_state. It returns the Ack so a
// caller can react (e.g. begin an orderly drain). Safe to call from the beat loop only.
func (c *Client) Beat(ctx context.Context) (Ack, error) {
	c.mu.Lock()
	b := Beat{
		WorkerID: c.workerID, Kind: c.kind, Region: c.region, Queue: c.queue, Version: c.version,
		Status: c.reportedStatus(), JobsActive: c.jobsActive, JobsDone: c.jobsDone,
	}
	c.mu.Unlock()

	ack, err := c.transport.Send(ctx, b)
	if err != nil {
		return Ack{}, err
	}
	c.mu.Lock()
	if ack.DesiredState != "" {
		c.desired = ack.DesiredState
	}
	c.mu.Unlock()
	return ack, nil
}

// Run beats every interval until ctx is cancelled or the worker converges to stopped (Done). It
// is the convergence loop enrichd runs alongside its claim loop; a returning Run means "exit".
func (c *Client) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	if _, err := c.Beat(ctx); err == nil && c.Done() {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := c.Beat(ctx); err != nil {
				c.log.Warn("heartbeat failed", "worker", c.workerID, "err", err)
				continue
			}
			if c.Done() {
				return nil
			}
		}
	}
}
