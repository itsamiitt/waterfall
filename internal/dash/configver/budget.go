package configver

import (
	"context"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// BudgetReader reads the Class-T budgets table (migration 0006, moved here per Deviation D-2) for
// the VR-7 cost cross-check. It is READ-ONLY here; the budgets table is owned by internal/dash/cost
// once it lands (P6). Reads run under the caller's Principal, so RLS scopes them to the tenant.
type BudgetReader struct {
	store *db.Store
}

// NewBudgetReader wires a BudgetReader to the shared db.Store.
func NewBudgetReader(store *db.Store) *BudgetReader { return &BudgetReader{store: store} }

var _ BudgetSource = (*BudgetReader)(nil)

// Limit returns the limit_credits for the caller-tenant budget row (scope, scope_key, period), if
// present. When two periods exist, VR-7 checks against the row the caller names.
func (r *BudgetReader) Limit(ctx context.Context, scope, scopeKey, period string) (int64, bool, error) {
	var limit int64
	found := false
	err := r.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select limit_credits from budgets
			   where scope=$1 and scope_key=$2 and period=$3`, scope, scopeKey, period)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			limit, _ = strconv.ParseInt(*res.Rows[0][0], 10, 64)
			found = true
		}
		return nil
	})
	return limit, found, err
}
