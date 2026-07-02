// Package store defines the persistence contracts for the engine and an in-memory
// implementation. Every method is tenant-scoped: the tenant is read from the context
// principal (gate G1), never passed as an argument, so no caller can address another
// tenant's partition by supplying a different id.
//
// The interfaces model the three correctness-critical tables from docs/06:
//   - IdempotencyLedger  -> gate G2 (exactly-once-effective provider calls)
//   - CostLedger         -> gate G4 (cost ceiling reserved before execution)
//   - FieldVersions      -> gate G5 (append-only provenance; losers retained)
package store

import (
	"context"
	"errors"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// ErrCeilingExceeded is returned by CostLedger.Reserve when a reservation would push a
// job's committed spend past its ceiling (G4). It is not an error condition to log
// loudly — it is the gate working — so the engine treats it as "stop, don't fail".
var ErrCeilingExceeded = errors.New("store: cost ceiling would be exceeded")

// errInvalidFieldValue backs the G5 rejection when Append is handed a bare,
// provenance-less value.
var errInvalidFieldValue = errors.New("store: field value fails G5 validity (missing value or provenance)")

// IdempotencyLedger records the terminal result of each provider call keyed by its G2
// idempotency key, so a replay returns the stored result with no second paid call.
type IdempotencyLedger interface {
	// Lookup returns a prior terminal result for key, if any, within the caller's tenant.
	Lookup(ctx context.Context, key string) (provider.Result, bool, error)
	// Record stores the terminal result for key. Recording an existing key is a no-op
	// (first writer wins) so concurrent retries converge.
	Record(ctx context.Context, key string, res provider.Result) error
}

// CostLedger enforces the per-job cost ceiling atomically (G4). Reserve must be called
// and must succeed BEFORE a paid provider call is made.
type CostLedger interface {
	// Reserve atomically adds amount to jobID's committed spend iff the new total does
	// not exceed ceiling, returning the new committed total. If it would exceed, it
	// makes no change and returns ErrCeilingExceeded. Reserve is called BEFORE a paid
	// provider call so the ceiling is never breached even transiently.
	Reserve(ctx context.Context, jobID string, amount, ceiling domain.Credits) (domain.Credits, error)
	// Release refunds a prior reservation for jobID (never below zero). Used to model
	// charge-on-success: a reservation guards the ceiling before the call, and is
	// released if the call fails or returns no billable data.
	Release(ctx context.Context, jobID string, amount domain.Credits) error
	// Committed returns the amount charged so far for jobID within the tenant.
	Committed(ctx context.Context, jobID string) (domain.Credits, error)
}

// FieldVersions is the append-only provenance store (G5). Every observation is retained
// (winners and losers, ADR-0006); Current returns the highest-confidence value per Field.
type FieldVersions interface {
	// Append stores a resolved FieldValue. It MUST reject a value for which
	// FieldValue.Valid() is false (no bare, provenance-less writes) — that is the G5
	// enforcement point.
	Append(ctx context.Context, subjectID string, v domain.FieldValue) error
	// Current returns the current best value per Field for a subject within the tenant.
	Current(ctx context.Context, subjectID string) (map[domain.Field]domain.FieldValue, error)
	// History returns every retained observation for a subject+field (winners+losers).
	History(ctx context.Context, subjectID string, f domain.Field) ([]domain.FieldValue, error)
}

// Store bundles the three ledgers a single backend provides.
type Store interface {
	IdempotencyLedger
	CostLedger
	FieldVersions
}
