package alerts

import (
	"fmt"
	"sort"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// anomalyAbsFloor is the DEFAULT absolute-credits floor of the cost.anomaly DUAL threshold (doc 12
// §P6, doc 10 §4): today must exceed the same-day-of-week median by BOTH the rule's percent
// threshold AND this many credits, so a large relative spike on a near-zero baseline does not fire.
// A rule may override it per-rule via alert_rules.anomaly_floor_credits (OI-P6-3).
const anomalyAbsFloor = 1000

// anomalyFloor resolves the absolute-credits floor for a rule: its per-rule override when set,
// otherwise the package default (OI-P6-3, doc 10 §4).
func anomalyFloor(r Rule) float64 {
	if r.AnomalyFloorCredits != nil {
		return float64(*r.AnomalyFloorCredits)
	}
	return anomalyAbsFloor
}

// anomalyBreaches is the pure DUAL-threshold decision for cost.anomaly: today breaches only when the
// percent increase over the same-day-of-week median meets the rule's threshold AND the absolute
// credit delta clears the (per-rule or default) floor. Kept I/O-free so OI-P6-3's unit test drives
// it directly.
func anomalyBreaches(r Rule, pctIncrease, creditDelta float64) bool {
	return pctIncrease >= r.Threshold && creditDelta >= anomalyFloor(r)
}

// costAnomaly compares today's spend to the trailing-28d same-day-of-week median (4 prior samples)
// in the rule's provider/workflow scope. It breaches when the increase exceeds BOTH rule.Threshold
// percent AND the rule's absolute-credits floor (per-rule override or the package default; see
// anomalyBreaches). The reported value is the percent increase over the median.
func costAnomaly(c *pg.Conn, r Rule, now time.Time) (metricResult, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	scopeWhere, scopeArgs := costScopeWhere(r)

	todayCredits, err := sumDay(c, today, scopeWhere, scopeArgs)
	if err != nil {
		return metricResult{}, err
	}
	// Same weekday for the prior 4 weeks.
	samples := make([]float64, 0, 4)
	for w := 1; w <= 4; w++ {
		d := today.AddDate(0, 0, -7*w)
		v, err := sumDay(c, d, scopeWhere, scopeArgs)
		if err != nil {
			return metricResult{}, err
		}
		samples = append(samples, v)
	}
	if len(samples) == 0 {
		return metricResult{hasData: false}, nil
	}
	med := median(samples)
	var pctIncrease float64
	if med > 0 {
		pctIncrease = (todayCredits - med) / med * 100
	} else if todayCredits > 0 {
		pctIncrease = 100
	}
	breach := anomalyBreaches(r, pctIncrease, todayCredits-med)
	return metricResult{value: pctIncrease, breaching: breach, hasData: true}, nil
}

// budgetBurnPct is SUM(credits) for the budget's UTC period vs budgets.limit_credits, as a percent
// (doc 10 §4). The rule scope carries {scope, scope_key, period}; without a matching budget row
// there is no threshold to burn against (hasData=false), which keeps forecast-budget alerts silent.
func budgetBurnPct(c *pg.Conn, r Rule, now time.Time) (metricResult, error) {
	scope := r.Scope["scope"]
	scopeKey := r.Scope["scope_key"]
	period := r.Scope["period"]
	if scope == "" || period == "" {
		return metricResult{hasData: false}, nil
	}
	res, err := c.QueryParams(
		`select limit_credits from budgets where scope=$1 and scope_key=$2 and period=$3`,
		scope, scopeKey, period)
	if err != nil {
		return metricResult{}, err
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return metricResult{hasData: false}, nil
	}
	limit := f64(res.Rows[0][0])
	if limit <= 0 {
		return metricResult{hasData: false}, nil
	}
	start := budgetPeriodStart(period, now)
	next := budgetPeriodNext(period, start)
	where := "day >= $1 and day < $2"
	args := []any{start.Format("2006-01-02"), next.Format("2006-01-02")}
	switch scope {
	case "provider":
		where += " and provider_id = $3"
		args = append(args, scopeKey)
	case "workflow":
		where += " and workflow_key = $3"
		args = append(args, scopeKey)
	}
	cres, err := c.QueryParams(`select coalesce(sum(credits),0) from cost_rollup_1d where `+where, args...)
	if err != nil {
		return metricResult{}, err
	}
	consumed := f64(cres.Rows[0][0])
	pct := consumed / limit * 100
	return metricResult{value: pct, breaching: compare(r.Op, pct, r.Threshold), hasData: true}, nil
}

// topContributors returns up to 3 providers ranked by their credit increase vs the same-day-of-week
// median (for the cost.anomaly episode payload / rule-test response). Best-effort; a query error
// returns nil.
func topContributors(c *pg.Conn, r Rule, now time.Time) []map[string]any {
	today := now.UTC().Truncate(24 * time.Hour)
	res, err := c.QueryParams(
		`select provider_id, coalesce(sum(credits),0) from cost_rollup_1d where day = $1 group by provider_id`,
		today.Format("2006-01-02"))
	if err != nil {
		return nil
	}
	type row struct {
		provider string
		delta    float64
	}
	var rows []row
	for _, rr := range res.Rows {
		p := str(rr[0])
		var samples []float64
		for w := 1; w <= 4; w++ {
			d := today.AddDate(0, 0, -7*w)
			pr, perr := c.QueryParams(
				`select coalesce(sum(credits),0) from cost_rollup_1d where day=$1 and provider_id=$2`,
				d.Format("2006-01-02"), p)
			if perr != nil {
				continue
			}
			samples = append(samples, f64(pr.Rows[0][0]))
		}
		rows = append(rows, row{provider: p, delta: f64(rr[1]) - median(samples)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].delta > rows[j].delta })
	out := make([]map[string]any, 0, 3)
	for i := 0; i < len(rows) && i < 3; i++ {
		out = append(out, map[string]any{"provider_id": rows[i].provider, "credit_delta": rows[i].delta})
	}
	return out
}

// costScopeWhere builds the cost_rollup_1d provider/workflow scope filter (starting at $2, since $1
// is the day parameter in sumDay).
func costScopeWhere(r Rule) (string, []any) {
	where := ""
	var args []any
	if p := r.Scope["provider_id"]; p != "" {
		args = append(args, p)
		where += fmt.Sprintf(" and provider_id = $%d", len(args)+1)
	}
	if wf := r.Scope["workflow_key"]; wf != "" {
		args = append(args, wf)
		where += fmt.Sprintf(" and workflow_key = $%d", len(args)+1)
	}
	return where, args
}

// sumDay sums cost_rollup_1d.credits for one UTC day plus a scope filter.
func sumDay(c *pg.Conn, day time.Time, scopeWhere string, scopeArgs []any) (float64, error) {
	args := append([]any{day.Format("2006-01-02")}, scopeArgs...)
	res, err := c.QueryParams(`select coalesce(sum(credits),0) from cost_rollup_1d where day = $1`+scopeWhere, args...)
	if err != nil {
		return 0, err
	}
	return f64(res.Rows[0][0]), nil
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func budgetPeriodStart(period string, now time.Time) time.Time {
	now = now.UTC()
	if period == "month" {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func budgetPeriodNext(period string, start time.Time) time.Time {
	if period == "month" {
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 1)
}
