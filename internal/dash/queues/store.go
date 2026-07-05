// Package queues is the dashboard's engine-agnostic read model over the production queue
// (the pgoutbox transactional outbox, `job_outbox`) plus two narrow write verbs: single
// redrive and filtered bulk replay (doc 06, doc 04 §2.8). It never writes `job_outbox`
// directly — one-owner-per-table: reads go through the RLS-scoped db.Store, and the only
// mutation is delegated to the pgoutbox Redrive API under the caller's Principal.
//
// The vocabulary is CLOSED (QS-TMP-1 hedge, doc 06 §1.3): panels bind to the engine-agnostic
// state vector (waiting/running/scheduled/delayed/retry/failed/dead) and a Go interface, never
// to job_outbox column names, so swapping the engine means a new store, not a new panel.
//
// Gates: G1 tenant isolation — job_outbox, bulk_jobs are tenant-scoped (FORCE RLS); a Tenant
// lists/inspects/redrives only its own rows and cross-Tenant existence reads as not-found.
// queue_defs/queue_stats_1m are Class P (platform-only). Every list is keyset-cursored and
// limit-capped (doc 04 §1.4). No PII/secrets in logs or job-detail payloads.
package queues

import (
	"context"
	"errors"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// Sentinel errors the HTTP layer maps to uniform error bodies (doc 04 §1.6).
var (
	// ErrNotFound — no such row for this Tenant (redrive of a non-dead/absent job, missing job).
	ErrNotFound = errors.New("queues: not found")
	// ErrInvalidFilter — an out-of-vocabulary state/filter value (400 invalid_filter).
	ErrInvalidFilter = errors.New("queues: invalid filter")
	// ErrWindowOutOfRange — a stats window beyond the resolution's retention (400 window_out_of_range).
	ErrWindowOutOfRange = errors.New("queues: window out of range")
	// ErrReplayInFlight — an active replay job already exists for this queue (409 bulk_job_conflict).
	ErrReplayInFlight = errors.New("queues: a replay is already in flight for this queue")
)

// State is the engine-agnostic job-state vocabulary (doc 04 §2.8, doc 06 §2.2). It is closed:
// scheduled/delayed are always 0 on pgoutbox (reserved for target engines).
type State string

const (
	StateWaiting   State = "waiting"
	StateRunning   State = "running"
	StateScheduled State = "scheduled"
	StateDelayed   State = "delayed"
	StateRetry     State = "retry"
	StateFailed    State = "failed"
	StateDead      State = "dead"
)

// validStates is the closed enum for the required `state` filter on GET /queues/{name}/jobs.
var validStates = map[State]bool{
	StateWaiting: true, StateRunning: true, StateScheduled: true, StateDelayed: true,
	StateRetry: true, StateFailed: true, StateDead: true,
}

// ValidState reports whether s is a member of the closed state vocabulary.
func ValidState(s State) bool { return validStates[s] }

// defaultVisibilitySeconds mirrors pgoutbox.NewRelay's default reclaim window (doc 06 §1.1):
// a claimed row older than this is re-claimable and reads as `retry`, not `running`.
const defaultVisibilitySeconds = 30

// QueueSummary is one queue_defs row joined with its live state-count vector and oldest_age_s
// from the aggregator's last folded sample (never a per-request COUNT(*), doc 06 §2.1).
type QueueSummary struct {
	Name            string
	Kind            string
	MaxAttempts     int
	VisibilityS     int
	Description     string
	DesiredReplicas *int64 // scale intent (doc 06 §5); nil when unset
	// Live vector (from the newest queue_stats_1m bucket, ≤ one sample old).
	Depth      int64
	Running    int64
	Scheduled  int64
	Delayed    int64
	Retry      int64
	Failed     int64
	Dead       int64
	Enq        int64
	Deq        int64
	OldestAgeS int64
	SampleAt   time.Time // bucket_start of the sample, zero when no sample yet
}

// StatsBucket is one folded queue_stats_{1m,1h} row (the bounded time series, doc 04 §1.8).
type StatsBucket struct {
	BucketStart time.Time
	Depth       int64
	Running     int64
	Scheduled   int64
	Delayed     int64
	Retry       int64
	Failed      int64
	Dead        int64
	Enq         int64
	Deq         int64
	OldestAgeS  int64
}

// Window selects a stats resolution and range. Res is "1m" or "1h" (clamped server-side).
type Window struct {
	Res  string
	From time.Time
	To   time.Time
}

// JobRow is one Enrichment Job projected into an engine-agnostic state (payload NOT included;
// GET /jobs/{id} returns the redacted detail).
type JobRow struct {
	JobID     string
	State     State
	Status    string // job.Status: queued|running|succeeded|failed
	Attempts  int
	Dead      bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DeadLetterRow is one parked (dead=true) job surfaced for inspection (doc 06 §3.1).
type DeadLetterRow struct {
	JobID     string
	Status    string
	Attempts  int
	LastError string
	UpdatedAt time.Time
	CreatedAt time.Time
}

// DeadFilter narrows the dead-letter list / replay scope (doc 04 §2.8). ErrorClass matches the
// park reason recorded in last_error (the 8-class taxonomy lives in the payload for the target
// engines; on pgoutbox last_error is the record). Before/After bound updated_at.
type DeadFilter struct {
	ErrorClass string
	Before     time.Time
	After      time.Time
}

// JobDetail is the GET /jobs/{id} response: operational fields + a redacted request summary so
// operators see WHY it died before replaying (doc 06 §3.1). The captured Principal and any
// secret material in the payload are never surfaced (doc 05 §7.3).
type JobDetail struct {
	JobID      string
	Status     string
	State      State
	Attempts   int
	Dead       bool
	Pending    bool
	LastError  string
	SubjectID  string
	WantFields []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ClaimedAt  *time.Time
}

// ReplayJob is the durable bulk-replay progress record (bulk_jobs row) served to pollers
// (doc 04 §4 / §2.8). Per-item outcomes ∈ redriven | skipped_not_dead | error.
type ReplayJob struct {
	ID                 string
	Kind               string
	Status             string
	Total              int
	Succeeded          int
	Failed             int
	MatchedAtExecution int
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	Results            []ReplayItem
}

// ReplayItem is one row's replay outcome.
type ReplayItem struct {
	JobID   string `json:"job_id"`
	Outcome string `json:"outcome"`
}

// BulkJob is the kind-agnostic durable bulk_jobs record served at GET /bulk-jobs/{id} — the ONE
// durable poller for every 202 bulk operation (replay, rolling_restart, …). Results is the raw
// per-item jsonb so the reader stays kind-agnostic (doc 04 §4 / §1.7).
type BulkJob struct {
	ID                 string
	Kind               string
	Status             string
	Total              int
	Succeeded          int
	Failed             int
	MatchedAtExecution int
	Results            []byte
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
}

// Replay outcome vocabulary (doc 06 §3.4).
const (
	OutcomeRedriven       = "redriven"
	OutcomeSkippedNotDead = "skipped_not_dead"
	OutcomeError          = "error"
)

// --- consumer-side interfaces (doc 06 §1.3); repo style: small ifaces + var _ assertions ---

// QueueStats is the platform-level read: queue registry + live vector, and the bounded series.
type QueueStats interface {
	Queues(ctx context.Context) ([]QueueSummary, error)
	Stats(ctx context.Context, queue string, w Window) ([]StatsBucket, error)
}

// JobLister lists jobs in one required state and parked dead letters, both cursor-paginated.
type JobLister interface {
	Jobs(ctx context.Context, queue string, state State, cur db.Cursor, limit int) ([]JobRow, db.Cursor, error)
	DeadLetters(ctx context.Context, f DeadFilter, cur db.Cursor, limit int) ([]DeadLetterRow, db.Cursor, error)
}

// Redriver is the two-verb write surface: single redrive (delegated to pgoutbox) and filtered
// bulk replay (a 202 bulk job). Redrive returns false when no dead row matched (idempotent no-op).
type Redriver interface {
	Redrive(ctx context.Context, jobID string) (bool, error)
	Replay(ctx context.Context, queue string, f DeadFilter) (string, error)
}

// QueueBackend is everything the queue panels need from a queue engine (doc 06 §1.3). pgstore
// satisfies it over job_outbox + queue_stats_1m today; a kafkastore/temporalstore later.
type QueueBackend interface {
	QueueStats
	JobLister
	Redriver
}

// OutboxRedriver is the ONE write verb the queues feature delegates to pgoutbox — the dashboard
// never writes job_outbox directly (one-owner-per-table, doc 06 §2.4). pgoutbox.Store satisfies it.
type OutboxRedriver interface {
	Redrive(ctx context.Context, jobID string) (bool, error)
}

var _ QueueBackend = (*Service)(nil)
