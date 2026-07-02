// Package pgoutbox is a PostgreSQL transactional-outbox durable job queue (docs/10 §4,
// docs/35). It is a drop-in replacement for the file-WAL durable store (internal/durable):
// it implements job.Store + job.Submitter, persisting each job as a row in `job_outbox`, and
// a Relay claims pending rows with FOR UPDATE SKIP LOCKED and feeds them to the worker pool.
//
// Durability model (mirrors internal/durable): Submit writes the job row with pending=true;
// Put clears pending ONLY on a terminal state, in the same UPDATE as the terminal snapshot.
// A crash before that leaves the row pending, so the Relay re-drives it (at-least-once); the
// engine's G2 idempotency makes redelivery free of double effect.
//
// Tenant scope (G1): Submit/Put/Get set the tenant GUC from the request principal and are
// RLS-scoped. The Relay (a system consumer) uses a separate BYPASSRLS connection to claim
// across tenants; tenant identity is preserved on each row and flows into execution via the
// job's captured principal.
package pgoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// ErrTenantMismatch is returned when a job's TenantID does not match the caller's principal.
var ErrTenantMismatch = errors.New("pgoutbox: job tenant does not match principal")

// Store is a Postgres-backed job.Store + job.Submitter.
type Store struct {
	pool *pg.Pool
}

var (
	_ job.Store     = (*Store)(nil)
	_ job.Submitter = (*Store)(nil)
)

// New wraps a connection pool (tenant-scoped role).
func New(pool *pg.Pool) *Store { return &Store{pool: pool} }

// Open validates connectivity and returns a Store over a pool of up to maxConns connections.
func Open(cfg pg.Config, maxConns int) (*Store, error) {
	c, err := pg.Connect(cfg)
	if err != nil {
		return nil, err
	}
	c.Close()
	return New(pg.NewPool(cfg, maxConns)), nil
}

// Close closes the pool.
func (s *Store) Close() error { s.pool.Close(); return nil }

func (s *Store) tx(ctx context.Context, fn func(*pg.Conn) error) error {
	ten, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	c, err := s.pool.Get(ctx)
	if err != nil {
		return err
	}
	broken := false
	defer func() { s.pool.Put(c, broken) }()
	if err := c.Exec("begin"); err != nil {
		broken = true
		return err
	}
	if err := c.ExecParams("select set_config('app.current_tenant', $1, true)", ten); err != nil {
		_ = c.Exec("rollback")
		return err
	}
	if ferr := fn(c); ferr != nil {
		_ = c.Exec("rollback")
		return ferr
	}
	if err := c.Exec("commit"); err != nil {
		broken = true
		return err
	}
	return nil
}

// Submit durably captures a queued job (pending=true). Re-submitting an existing job id is a
// no-op (idempotent), so it never sheds — durability, not back-pressure, is the point.
func (s *Store) Submit(ctx context.Context, j *job.Job) (bool, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return false, err
	}
	if j.TenantID != t {
		return false, ErrTenantMismatch
	}
	j.Status = job.StatusQueued
	payload, err := json.Marshal(j)
	if err != nil {
		return false, err
	}
	err = s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into job_outbox (job_id, tenant_id, payload, status, pending)
			values ($1, current_setting('app.current_tenant'), $2::jsonb, $3, true)
			on conflict (job_id) do nothing`, j.ID, string(payload), string(j.Status))
	})
	return err == nil, err
}

// Put upserts a job's state. pending is cleared iff the status is terminal — in the same
// UPDATE as the snapshot — so a crash before durable-terminal leaves the row re-drivable.
func (s *Store) Put(ctx context.Context, j *job.Job) error {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	if j.TenantID != t {
		return ErrTenantMismatch
	}
	payload, err := json.Marshal(j)
	if err != nil {
		return err
	}
	pending := !isTerminal(j.Status)
	return s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into job_outbox (job_id, tenant_id, payload, status, pending)
			values ($1, current_setting('app.current_tenant'), $2::jsonb, $3, $4)
			on conflict (job_id) do update set
				payload = excluded.payload, status = excluded.status,
				pending = excluded.pending, updated_at = now()`,
			j.ID, string(payload), string(j.Status), pending)
	})
}

// Get returns a job by id within the caller's tenant (RLS-scoped; another tenant's job reads
// as not found).
func (s *Store) Get(ctx context.Context, id string) (*job.Job, bool, error) {
	var out *job.Job
	found := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams("select payload from job_outbox where job_id = $1", id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		var j job.Job
		if err := json.Unmarshal([]byte(*res.Rows[0][0]), &j); err != nil {
			return err
		}
		out = &j
		found = true
		return nil
	})
	return out, found, err
}

// DeadLetter is a parked (poison) job surfaced for inspection.
type DeadLetter struct {
	JobID     string
	Status    string
	Attempts  int
	LastError string
	UpdatedAt string
}

// DeadLetters returns the caller tenant's dead-lettered jobs, newest first (RLS-scoped: a
// tenant sees only its own). limit is clamped to [1,500].
func (s *Store) DeadLetters(ctx context.Context, limit int) ([]DeadLetter, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []DeadLetter
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select job_id, status, attempts, coalesce(last_error, ''), updated_at
			from job_outbox where dead order by updated_at desc limit $1::int`, limit)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			dl := DeadLetter{}
			if row[0] != nil {
				dl.JobID = *row[0]
			}
			if row[1] != nil {
				dl.Status = *row[1]
			}
			if row[2] != nil {
				dl.Attempts, _ = strconv.Atoi(*row[2])
			}
			if row[3] != nil {
				dl.LastError = *row[3]
			}
			if row[4] != nil {
				dl.UpdatedAt = *row[4]
			}
			out = append(out, dl)
		}
		return nil
	})
	return out, err
}

// Redrive resets a dead-lettered job so the relay re-delivers it (dead=false, pending=true,
// attempts=0, claim + error cleared) — used after the underlying bug is fixed. RLS-scoped: a
// tenant can only redrive its OWN parked job. Returns true iff a dead row was reset (false =
// no such job for this tenant, or it was not dead). The payload is untouched, so the same job
// re-executes; G2 idempotency makes any partial prior effect safe.
func (s *Store) Redrive(ctx context.Context, jobID string) (bool, error) {
	found := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`update job_outbox set
				dead = false, pending = true, attempts = 0,
				claimed_at = null, last_error = null, status = 'queued', updated_at = now()
			where job_id = $1 and dead
			returning job_id`, jobID)
		if err != nil {
			return err
		}
		found = len(res.Rows) > 0 && res.Rows[0][0] != nil
		return nil
	})
	return found, err
}

func isTerminal(s job.Status) bool {
	return s == job.StatusSucceeded || s == job.StatusFailed
}
