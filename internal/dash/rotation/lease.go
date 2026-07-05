package rotation

import (
	"context"
	"errors"
	"sync"
)

// batchSize is the maximum number of quota tokens leased from key_budgets per DB refill (doc 07 §8
// "batch <= 64"). A crash therefore loses AT MOST one batch of leased-but-unused tokens; those are
// reclaimed at the daily rollover (day_leased resets) and day_used is corrected by Reconcile.
const batchSize = 64

// ErrQuotaExhausted is returned by a token draw when the key's daily_limit is fully leased. The
// lease loop treats it as a per-key failover signal (mark the key exhausted, try the next key).
var ErrQuotaExhausted = errors.New("rotation: key daily quota exhausted")

// ErrUsageEventsAbsent reports that the usage_events table does not exist yet (it lands in P4).
// Reconcile treats it as a no-op so P2 can ship the reconcile path against the documented columns.
var ErrUsageEventsAbsent = errors.New("rotation: usage_events table not present")

// leaser draws a batch of quota tokens under the guarded atomic UPDATE key_budgets ... WHERE
// day_leased + $2 <= daily_limit RETURNING (satisfied by *pgStore). Returning granted <= 0 means
// the daily limit is reached. Because every refill is bounded by the guard, the sum of granted
// tokens can never exceed daily_limit — the no-over-lease invariant (doc 12 P2 #1).
type leaser interface {
	LeaseBatch(ctx context.Context, keyID string, batch int, dailyLimit int64) (granted int, err error)
}

// bucket is one key's in-memory token bucket. tokens are consumed without a DB round-trip; a refill
// (a single guarded UPDATE) happens only when the bucket empties, so the DB write rate is ~rps/64
// per key worst case (doc 07 §8). The selector NEVER does a per-request DB write.
type bucket struct {
	mu         sync.Mutex
	tokens     int
	dailyLimit int64
}

// bucketRegistry owns the per-key token buckets and the shared leaser.
type bucketRegistry struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	leaser  leaser
	batch   int
}

func newBucketRegistry(l leaser) *bucketRegistry {
	return &bucketRegistry{buckets: map[string]*bucket{}, leaser: l, batch: batchSize}
}

func (r *bucketRegistry) bucketFor(keyID string, dailyLimit int64) *bucket {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buckets[keyID]
	if b == nil {
		b = &bucket{dailyLimit: dailyLimit}
		r.buckets[keyID] = b
	} else {
		b.dailyLimit = dailyLimit
	}
	return b
}

// draw takes exactly one lease token for keyID. It returns nil on success, ErrQuotaExhausted when
// the daily limit is reached, or a DB error. A dailyLimit <= 0 means "unlimited" — granted without
// any DB accounting. The per-bucket mutex makes concurrent draws on one key correct within an
// instance; the guarded UPDATE makes them correct across instances.
func (r *bucketRegistry) draw(ctx context.Context, keyID string, dailyLimit int64) error {
	if dailyLimit <= 0 {
		return nil
	}
	b := r.bucketFor(keyID, dailyLimit)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens > 0 {
		b.tokens--
		return nil
	}
	granted, err := r.leaser.LeaseBatch(ctx, keyID, r.batch, dailyLimit)
	if err != nil {
		return err
	}
	if granted <= 0 {
		return ErrQuotaExhausted
	}
	b.tokens = granted - 1 // consume one now; hold the rest for subsequent draws
	return nil
}

// reconcileStore is the narrow seam Reconcile depends on (satisfied by *pgStore and test fakes).
type reconcileStore interface {
	// UsageDayTotals returns today's per-key ground-truth usage (sum of usage_events.credits by
	// key_id). It returns ErrUsageEventsAbsent when the table does not exist yet (P4).
	UsageDayTotals(ctx context.Context) (map[string]int64, error)
	// SetBudgetDayUsed rewrites key_budgets.day_used to the given ground-truth totals.
	SetBudgetDayUsed(ctx context.Context, totals map[string]int64) error
}

// reconcile rewrites key_budgets.day_used from the usage_events ground truth (doc 07 §8 "nightly
// reconcile rewrites day_used from usage_events"). It is a no-op (0, nil) when usage_events is
// absent (pre-P4) so the path ships now and is exercised over synthetic rows. It never touches
// day_leased: the crash-lost lease inflation self-heals at the daily rollover.
func reconcile(ctx context.Context, s reconcileStore) (int, error) {
	totals, err := s.UsageDayTotals(ctx)
	if err != nil {
		if errors.Is(err, ErrUsageEventsAbsent) {
			return 0, nil
		}
		return 0, err
	}
	if len(totals) == 0 {
		return 0, nil
	}
	if err := s.SetBudgetDayUsed(ctx, totals); err != nil {
		return 0, err
	}
	return len(totals), nil
}
