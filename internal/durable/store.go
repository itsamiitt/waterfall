package durable

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Record kinds persisted by the store.
const (
	kindJob             = "job"              // a full job snapshot (latest state)
	kindOutboxPending   = "outbox_pending"   // publish-intent for a job id (not yet on the queue)
	kindOutboxPublished = "outbox_published" // the intent has been delivered to the queue
)

// ErrTenantMismatch guards against writing a job under the wrong tenant scope.
var ErrTenantMismatch = errors.New("durable: job tenant does not match context principal")

// Store is a crash-safe job.Store + job.Submitter backed by the Log. Submitting a job
// atomically appends the job snapshot AND an outbox publish-intent in one committed batch
// (the transactional outbox): there is no window where a job is persisted but its
// intent-to-run is lost, or vice versa. The Relay later moves pending intents onto the
// queue; redeliveries are deduped by the engine (G2).
type Store struct {
	log *Log

	mu      sync.Mutex
	all     map[string]*job.Job // id -> latest snapshot (internal, for the relay)
	pending map[string]struct{} // ids with an unpublished outbox intent
}

// OpenStore opens the durable store at path, recovering all jobs and pending outbox
// intents from the log (crash recovery).
func OpenStore(path string) (*Store, error) {
	s := &Store{all: map[string]*job.Job{}, pending: map[string]struct{}{}}
	log, err := Open(path, s.apply)
	if err != nil {
		return nil, err
	}
	s.log = log
	return s, nil
}

// Close closes the underlying log.
func (s *Store) Close() error { return s.log.Close() }

var (
	_ job.Store     = (*Store)(nil)
	_ job.Submitter = (*Store)(nil)
)

// apply mutates the in-memory index for one committed record during replay. It runs
// single-threaded inside Open, before the store is shared, so it needs no lock.
func (s *Store) apply(rec Record) error {
	switch rec.Kind {
	case kindJob:
		var j job.Job
		if err := json.Unmarshal(rec.Data, &j); err != nil {
			return err
		}
		cp := j
		s.all[j.ID] = &cp
	case kindOutboxPending:
		s.pending[unquote(rec.Data)] = struct{}{}
	case kindOutboxPublished:
		delete(s.pending, unquote(rec.Data))
	}
	return nil
}

// Submit is the durable async submission: it atomically persists the queued job and its
// outbox intent. It never sheds (durability, not back-pressure, is the point) so it
// always returns accepted=true on success.
func (s *Store) Submit(ctx context.Context, j *job.Job) (bool, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return false, err
	}
	if j.TenantID != t {
		return false, ErrTenantMismatch
	}
	j.Status = job.StatusQueued
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.log.Append(jobRecord(j), outboxRecord(kindOutboxPending, j.ID)); err != nil {
		return false, err
	}
	cp := *j
	s.all[j.ID] = &cp
	s.pending[j.ID] = struct{}{}
	return true, nil
}

// Put persists a job state transition durably (used by the Dispatcher for running/terminal
// updates). Tenant-scoped by the context principal (G1).
//
// The outbox intent is cleared ONLY when the job reaches a terminal state, and in the
// SAME atomic batch as the terminal snapshot. This makes EXECUTION crash-safe: if we
// crash at any point before a job is durably terminal, recovery still lists it as pending
// and the Relay re-drives it (at-least-once); the engine's G2 idempotency makes the
// redelivery free of double charges.
func (s *Store) Put(ctx context.Context, j *job.Job) error {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	if j.TenantID != t {
		return ErrTenantMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	recs := []Record{jobRecord(j)}
	terminal := isTerminal(j.Status)
	if terminal {
		recs = append(recs, outboxRecord(kindOutboxPublished, j.ID))
	}
	if err := s.log.Append(recs...); err != nil {
		return err
	}
	cp := *j
	s.all[j.ID] = &cp
	if terminal {
		delete(s.pending, j.ID)
	}
	return nil
}

func isTerminal(s job.Status) bool {
	return s == job.StatusSucceeded || s == job.StatusFailed
}

// Get returns a job by id within the caller's tenant. A job owned by another tenant is
// reported as not found (no cross-tenant disclosure — G1).
func (s *Store) Get(ctx context.Context, id string) (*job.Job, bool, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.all[id]
	if !ok || j.TenantID != t {
		return nil, false, nil
	}
	cp := *j
	return &cp, true, nil
}

// PendingOutbox returns copies of the jobs whose publish-intent has not yet been
// delivered to the queue. The Relay drains these.
func (s *Store) PendingOutbox() []*job.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*job.Job, 0, len(s.pending))
	for id := range s.pending {
		if j, ok := s.all[id]; ok {
			cp := *j
			out = append(out, &cp)
		}
	}
	return out
}

func jobRecord(j *job.Job) Record {
	data, _ := json.Marshal(j)
	return Record{Kind: kindJob, Data: data}
}

func outboxRecord(kind, id string) Record {
	data, _ := json.Marshal(id)
	return Record{Kind: kind, Data: data}
}

func unquote(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
