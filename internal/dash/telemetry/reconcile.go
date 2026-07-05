package telemetry

import (
	"context"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// Reconciler rewrites key_budgets.day_used from usage_events ground truth (doc 07 §8, doc 03 §6:
// "nightly reconcile rewrites day_used from usage_events"). This is the real telemetry-backed
// reconcile that supersedes rotation's P2 stub — it sums the raw feed per Provider Key across
// ALL Tenants (a key serves any Tenant's calls), which rotation's platform-only read could not
// do under usage_events tenant-isolation RLS. It never touches day_leased: crash-lost lease
// inflation self-heals at the daily rollover.
type Reconciler struct {
	store *db.Store
}

// NewReconciler builds a Reconciler over the dashboard Store.
func NewReconciler(store *db.Store) *Reconciler { return &Reconciler{store: store} }

// KeyDayTotals returns the ground-truth credits spent per Provider Key for the UTC day
// containing `day`, summed across every Tenant. Keys with no usage that day are absent.
func (r *Reconciler) KeyDayTotals(ctx context.Context, day time.Time) (map[string]int64, error) {
	from := day.UTC().Truncate(24 * time.Hour)
	to := from.AddDate(0, 0, 1)
	tenants, err := listCustomerTenants(ctx, r.store)
	if err != nil {
		return nil, err
	}
	totals := map[string]int64{}
	for _, tid := range tenants {
		tctx := principalFor(ctx, tid)
		txErr := r.store.Tx(tctx, func(c *pg.Conn) error {
			res, qerr := c.QueryParams(
				`select key_id, coalesce(sum(credits),0)
				   from usage_events
				  where created_at >= $1 and created_at < $2 and key_id is not null
				  group by key_id`, from, to)
			if qerr != nil {
				return qerr
			}
			for _, row := range res.Rows {
				if row[0] == nil {
					continue
				}
				totals[*row[0]] += i64(row[1])
			}
			return nil
		})
		if txErr != nil {
			return nil, txErr
		}
	}
	return totals, nil
}

// ReconcileKeyBudgets rewrites key_budgets.day_used to the usage_events ground truth for the UTC
// day containing `day`, and returns the number of keys rewritten. key_budgets is Class P
// (platform-only, doc 03 §2.2), so the write runs under PlatformTx. Keys with no usage are left
// untouched (a zero rewrite would need the key list; the daily rollover already zeroes them).
func (r *Reconciler) ReconcileKeyBudgets(ctx context.Context, day time.Time) (int, error) {
	totals, err := r.KeyDayTotals(ctx, day)
	if err != nil {
		return 0, err
	}
	if len(totals) == 0 {
		return 0, nil
	}
	err = r.store.PlatformTx(ctx, func(c *pg.Conn) error {
		for keyID, used := range totals {
			if err := c.ExecParams(
				`update key_budgets set day_used = $2::bigint, updated_at = now() where key_id = $1`,
				keyID, used); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(totals), nil
}
