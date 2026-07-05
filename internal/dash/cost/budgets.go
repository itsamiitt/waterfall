package cost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// Budget is one alerting budget row (doc 03 §2.6 / migration 0006). Doctrine: budgets ALERT, the
// G4 cost ceiling ENFORCES — this object never gates execution. current_period_start and
// consumed_credits are computed at read (UTC calendar period, §1.8/RF-4), not stored.
type Budget struct {
	Scope        string `json:"scope"`
	ScopeKey     string `json:"scope_key"`
	Period       string `json:"period"`
	LimitCredits int64  `json:"limit_credits"`
	AlertPct     []int  `json:"alert_pct"`

	CurrentPeriodStart *time.Time `json:"current_period_start,omitempty"`
	ConsumedCredits    int64      `json:"consumed_credits"`
}

// ErrInvalidBudget is returned when a budget item fails validation (bad scope/period/limit). The
// HTTP layer maps it to 422 validation_failed.
var ErrInvalidBudget = errors.New("cost: invalid budget item")

var validScopes = map[string]bool{"tenant": true, "provider": true, "workflow": true}
var validPeriods = map[string]bool{"day": true, "month": true}

// ListBudgets returns the Tenant's budgets with the current-period consumed spend attached.
func (s *Service) ListBudgets(ctx context.Context) ([]Budget, error) {
	var out []Budget
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query(
			`select scope, scope_key, period, limit_credits, alert_pct
			   from budgets order by scope, scope_key, period`)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			b := Budget{
				Scope:        str(r[0]),
				ScopeKey:     str(r[1]),
				Period:       str(r[2]),
				LimitCredits: i64(r[3]),
				AlertPct:     parseIntArray(str(r[4])),
			}
			if err := s.attachConsumed(c, &b); err != nil {
				return err
			}
			out = append(out, b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReplaceBudgets is the full-replacement PUT (doc 04 §2.10): it validates every item, deletes the
// Tenant's existing budget set, and inserts the new one — all in ONE RLS transaction (tenant_id
// comes from the bound Principal, never the body). It echoes the stored set with consumed spend.
func (s *Service) ReplaceBudgets(ctx context.Context, items []Budget) ([]Budget, error) {
	for i := range items {
		if err := validateBudget(items[i]); err != nil {
			return nil, err
		}
	}
	var out []Budget
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		if err := c.Exec("delete from budgets"); err != nil {
			return err
		}
		for _, it := range items {
			if err := c.ExecParams(
				`insert into budgets (tenant_id, scope, scope_key, period, limit_credits, alert_pct)
				 values (app_current_tenant(), $1, $2, $3, $4, $5::int[])`,
				it.Scope, it.ScopeKey, it.Period, it.LimitCredits, intArrayLiteral(it.AlertPct)); err != nil {
				return err
			}
		}
		res, qerr := c.Query(
			`select scope, scope_key, period, limit_credits, alert_pct
			   from budgets order by scope, scope_key, period`)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			b := Budget{
				Scope:        str(r[0]),
				ScopeKey:     str(r[1]),
				Period:       str(r[2]),
				LimitCredits: i64(r[3]),
				AlertPct:     parseIntArray(str(r[4])),
			}
			if err := s.attachConsumed(c, &b); err != nil {
				return err
			}
			out = append(out, b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// attachConsumed computes the UTC period start and the credits consumed so far this period for b,
// reading cost_rollup_1d under the already-bound RLS connection.
func (s *Service) attachConsumed(c *pg.Conn, b *Budget) error {
	start := periodStart(b.Period, s.now().UTC())
	next := periodNext(b.Period, start)
	b.CurrentPeriodStart = &start

	where := "day >= $1 and day < $2"
	args := []any{start.Format("2006-01-02"), next.Format("2006-01-02")}
	switch b.Scope {
	case "provider":
		where += " and provider_id = $3"
		args = append(args, b.ScopeKey)
	case "workflow":
		where += " and workflow_key = $3"
		args = append(args, b.ScopeKey)
	}
	res, err := c.QueryParams(`select coalesce(sum(credits),0) from cost_rollup_1d where `+where, args...)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		b.ConsumedCredits = i64(res.Rows[0][0])
	}
	return nil
}

// periodStart truncates now to the start of its UTC calendar day or month (RF-4 UTC latching).
func periodStart(period string, now time.Time) time.Time {
	now = now.UTC()
	if period == "month" {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// periodNext returns the start of the next UTC period after start.
func periodNext(period string, start time.Time) time.Time {
	if period == "month" {
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 1)
}

func validateBudget(b Budget) error {
	if !validScopes[b.Scope] {
		return fmt.Errorf("%w: scope %q", ErrInvalidBudget, b.Scope)
	}
	if !validPeriods[b.Period] {
		return fmt.Errorf("%w: period %q", ErrInvalidBudget, b.Period)
	}
	if b.LimitCredits < 0 {
		return fmt.Errorf("%w: limit_credits must be >= 0", ErrInvalidBudget)
	}
	if b.Scope != "tenant" && b.ScopeKey == "" {
		return fmt.Errorf("%w: scope_key required for scope %q", ErrInvalidBudget, b.Scope)
	}
	for _, p := range b.AlertPct {
		if p < 0 || p > 1000 {
			return fmt.Errorf("%w: alert_pct %d out of range", ErrInvalidBudget, p)
		}
	}
	return nil
}

// intArrayLiteral renders []int as a Postgres array literal '{a,b,c}' (the pg client sends params
// as text; the SQL casts it with ::int[]).
func intArrayLiteral(xs []int) string {
	if len(xs) == 0 {
		return "{}"
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// parseIntArray parses a Postgres int[] text rendering '{50,80,100}' back into []int.
func parseIntArray(s string) []int {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s == "{}" {
		return nil
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	var out []int
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		n := 0
		neg := false
		ok := true
		for i, ch := range tok {
			if ch == '-' && i == 0 {
				neg = true
				continue
			}
			if ch < '0' || ch > '9' {
				ok = false
				break
			}
			n = n*10 + int(ch-'0')
		}
		if ok {
			if neg {
				n = -n
			}
			out = append(out, n)
		}
	}
	return out
}
