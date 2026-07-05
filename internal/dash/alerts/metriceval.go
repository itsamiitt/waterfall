package alerts

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// metricResult is the outcome of evaluating one rule's metric for one cycle: the value to report on
// the episode, whether the rule breaches, and whether any source data was present (empty windows
// are not-breaching for rate/latency entries, doc 10 §5.1).
type metricResult struct {
	value     float64
	breaching bool
	hasData   bool
}

// latBucketsMs mirrors the telemetry histogram boundaries (doc 03 §2.6 single source of truth) so
// provider.p95_latency_ms can compute the percentile at read from lat_hist. Kept in lockstep with
// internal/dash/telemetry.event.latBucketsMs.
var latBucketsMs = [20]int64{
	1, 2, 4, 8, 16, 32, 64, 125, 250, 500,
	1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 1<<62 - 1,
}

// windowBuckets is ceil(window_s/60), the number of trailing 1m buckets the window covers (>=1).
func windowBuckets(windowS int) int {
	if windowS <= 60 {
		return 1
	}
	n := (windowS + 59) / 60
	if n > 200 {
		n = 200 // bounded read (doc 03 §4 limit cap)
	}
	return n
}

// evalMetric computes rule r's metric on connection c (already bound to the correct RLS scope —
// tenant for cost.*, platform for the Class-P sources) as of now. Unknown metrics or missing source
// tables surface as an error the evaluator logs-and-skips (never crashing the loop).
func evalMetric(c *pg.Conn, r Rule, now time.Time) (metricResult, error) {
	switch r.Metric {
	case "provider.error_rate":
		return windowRate(c, r, now, "provider_stats_1m", "(req - ok)::float8 / nullif(req,0)")
	case "provider.success_rate":
		return windowRate(c, r, now, "provider_stats_1m", "ok::float8 / nullif(req,0)")
	case "provider.p95_latency_ms":
		return p95Latency(c, r, now)
	case "provider.credits_remaining":
		return pointScalar(c, r, "select credits_remaining from providers where id = $1", r.Scope["provider_id"])
	case "key.credits_remaining":
		return keyAgg(c, r, "min(credits_remaining)")
	case "key.consecutive_failures":
		return keyAgg(c, r, "max(consecutive_failures)")
	case "key.active_ratio_in_pool":
		return activeRatio(c, r)
	case "queue.depth":
		return latestQueueBucket(c, r, now, "depth")
	case "queue.oldest_age_s":
		return latestQueueBucket(c, r, now, "oldest_age_s")
	case "queue.dead_count":
		return latestQueueBucket(c, r, now, "dead")
	case "worker.lost_count":
		return workerCount(c, r)
	case "worker.heartbeat_age_s":
		return workerHeartbeatAge(c, r, now)
	case "cost.daily_credits":
		return costDailyCredits(c, r, now)
	case "cost.budget_burn_pct":
		return budgetBurnPct(c, r, now)
	case "cost.anomaly":
		return costAnomaly(c, r, now)
	case "system.sse_clients":
		return pointScalar(c, r, "select coalesce(sum(sse_clients),0) from self_monitor", nil)
	case "system.aggregator_lag_s":
		return pointScalar(c, r,
			"select coalesce(extract(epoch from (now() - min(watermark_ts))),0) from self_monitor", nil)
	default:
		return metricResult{}, fmt.Errorf("%w: %q", ErrUnknownMetric, r.Metric)
	}
}

// windowRate evaluates a per-bucket ratio over the trailing window with N-of-M flap suppression:
// breach requires >= 2/3 of NON-EMPTY (req>0) buckets to breach (doc 10 §5.1). The reported value
// is the most recent non-empty bucket's ratio.
func windowRate(c *pg.Conn, r Rule, now time.Time, table, expr string) (metricResult, error) {
	n := windowBuckets(r.WindowS)
	from := now.Add(-time.Duration(n) * time.Minute)
	res, err := c.QueryParams(
		`select bucket_start, `+expr+` as v, req from `+table+`
		  where provider_id = $1 and bucket_start >= $2
		  order by bucket_start desc limit $3`,
		r.Scope["provider_id"], from.UTC(), int64(n))
	if err != nil {
		return metricResult{}, err
	}
	nonEmpty, breach := 0, 0
	var latest float64
	haveLatest := false
	for _, row := range res.Rows {
		if i64(row[2]) <= 0 {
			continue // empty bucket: not-breaching for rate entries
		}
		nonEmpty++
		v := f64(row[1])
		if !haveLatest {
			latest = v
			haveLatest = true
		}
		if compare(r.Op, v, r.Threshold) {
			breach++
		}
	}
	if nonEmpty == 0 {
		return metricResult{hasData: false}, nil
	}
	need := (2*nonEmpty + 2) / 3 // ceil(2/3 * nonEmpty)
	return metricResult{value: latest, breaching: breach >= need, hasData: true}, nil
}

// p95Latency sums the window's lat_hist into one histogram and reports the p95 bound in ms.
func p95Latency(c *pg.Conn, r Rule, now time.Time) (metricResult, error) {
	n := windowBuckets(r.WindowS)
	from := now.Add(-time.Duration(n) * time.Minute)
	res, err := c.QueryParams(
		`select lat_hist from provider_stats_1m
		  where provider_id = $1 and bucket_start >= $2 order by bucket_start desc limit $3`,
		r.Scope["provider_id"], from.UTC(), int64(n))
	if err != nil {
		return metricResult{}, err
	}
	var hist [20]int64
	var total int64
	for _, row := range res.Rows {
		for i, v := range parseBigintArray(str(row[0])) {
			if i < 20 {
				hist[i] += v
				total += v
			}
		}
	}
	if total == 0 {
		return metricResult{hasData: false}, nil
	}
	target := int64(math.Ceil(0.95 * float64(total)))
	var cum int64
	p95 := float64(latBucketsMs[19])
	for i, cnt := range hist {
		cum += cnt
		if cum >= target {
			p95 = float64(latBucketsMs[i])
			break
		}
	}
	return metricResult{value: p95, breaching: compare(r.Op, p95, r.Threshold), hasData: true}, nil
}

// pointScalar evaluates a single-row scalar query (point-in-time metric). arg may be nil for a
// no-parameter query.
func pointScalar(c *pg.Conn, r Rule, sql string, arg any) (metricResult, error) {
	var res *pg.Result
	var err error
	if arg == nil {
		res, err = c.Query(sql)
	} else {
		res, err = c.QueryParams(sql, arg)
	}
	if err != nil {
		return metricResult{}, err
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return metricResult{hasData: false}, nil
	}
	v := f64(res.Rows[0][0])
	return metricResult{value: v, breaching: compare(r.Op, v, r.Threshold), hasData: true}, nil
}

// keyAgg aggregates a provider_keys column over the rule's key scope (provider_id and/or pool_id).
func keyAgg(c *pg.Conn, r Rule, agg string) (metricResult, error) {
	where, args := keyScopeWhere(r)
	sql := `select ` + agg + ` from provider_keys k`
	if pool := r.Scope["pool_id"]; pool != "" {
		sql += ` join key_pool_members m on m.key_id = k.id`
	}
	sql += where
	res, err := c.QueryParams(sql, args...)
	if err != nil {
		return metricResult{}, err
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return metricResult{hasData: false}, nil
	}
	v := f64(res.Rows[0][0])
	return metricResult{value: v, breaching: compare(r.Op, v, r.Threshold), hasData: true}, nil
}

// activeRatio computes count(active)/count(*) for the pool.
func activeRatio(c *pg.Conn, r Rule) (metricResult, error) {
	pool := r.Scope["pool_id"]
	res, err := c.QueryParams(
		`select count(*) filter (where k.status='active')::float8 / nullif(count(*),0)
		   from provider_keys k join key_pool_members m on m.key_id = k.id
		  where m.pool_id = $1`, pool)
	if err != nil {
		return metricResult{}, err
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return metricResult{hasData: false}, nil
	}
	v := f64(res.Rows[0][0])
	return metricResult{value: v, breaching: compare(r.Op, v, r.Threshold), hasData: true}, nil
}

// latestQueueBucket reads the newest queue_stats_1m bucket's column within the window.
func latestQueueBucket(c *pg.Conn, r Rule, now time.Time, col string) (metricResult, error) {
	n := windowBuckets(r.WindowS)
	from := now.Add(-time.Duration(n) * time.Minute)
	res, err := c.QueryParams(
		`select `+col+` from queue_stats_1m where queue = $1 and bucket_start >= $2
		  order by bucket_start desc limit 1`,
		r.Scope["queue"], from.UTC())
	if err != nil {
		return metricResult{}, err
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return metricResult{hasData: false}, nil
	}
	v := f64(res.Rows[0][0])
	return metricResult{value: v, breaching: compare(r.Op, v, r.Threshold), hasData: true}, nil
}

// workerCount counts workers with status='lost' in the kind/queue scope.
func workerCount(c *pg.Conn, r Rule) (metricResult, error) {
	where, args := workerScopeWhere(r, "status = 'lost'")
	res, err := c.QueryParams(`select count(*) from workers`+where, args...)
	if err != nil {
		return metricResult{}, err
	}
	v := f64(res.Rows[0][0])
	return metricResult{value: v, breaching: compare(r.Op, v, r.Threshold), hasData: true}, nil
}

// workerHeartbeatAge is a staleness metric: absence of fresh rows IS the breach (doc 10 §5.1).
func workerHeartbeatAge(c *pg.Conn, r Rule, now time.Time) (metricResult, error) {
	where, args := workerScopeWhere(r, "status <> 'stopped'")
	res, err := c.QueryParams(
		`select coalesce(max(extract(epoch from ($1::timestamptz - last_heartbeat_at))), -1) from workers`+where,
		append([]any{now.UTC()}, args...)...)
	if err != nil {
		return metricResult{}, err
	}
	v := f64(res.Rows[0][0])
	if v < 0 { // no non-stopped workers: staleness => breach
		return metricResult{value: math.MaxInt32, breaching: true, hasData: true}, nil
	}
	return metricResult{value: v, breaching: compare(r.Op, v, r.Threshold), hasData: true}, nil
}

// costDailyCredits is SUM(credits) for the current UTC day in the provider/workflow scope.
func costDailyCredits(c *pg.Conn, r Rule, now time.Time) (metricResult, error) {
	day := now.UTC().Format("2006-01-02")
	where := "day = $1"
	args := []any{day}
	if p := r.Scope["provider_id"]; p != "" {
		args = append(args, p)
		where += fmt.Sprintf(" and provider_id = $%d", len(args))
	}
	if wf := r.Scope["workflow_key"]; wf != "" {
		args = append(args, wf)
		where += fmt.Sprintf(" and workflow_key = $%d", len(args))
	}
	res, err := c.QueryParams(`select coalesce(sum(credits),0) from cost_rollup_1d where `+where, args...)
	if err != nil {
		return metricResult{}, err
	}
	v := f64(res.Rows[0][0])
	return metricResult{value: v, breaching: compare(r.Op, v, r.Threshold), hasData: true}, nil
}

// keyScopeWhere builds the provider_keys WHERE clause + args for a key scope.
func keyScopeWhere(r Rule) (string, []any) {
	var conds []string
	var args []any
	if p := r.Scope["provider_id"]; p != "" {
		args = append(args, p)
		conds = append(conds, fmt.Sprintf("k.provider_id = $%d", len(args)))
	}
	if pool := r.Scope["pool_id"]; pool != "" {
		args = append(args, pool)
		conds = append(conds, fmt.Sprintf("m.pool_id = $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " where " + strings.Join(conds, " and "), args
}

// workerScopeWhere builds the workers WHERE clause + args for a base predicate plus kind/queue.
func workerScopeWhere(r Rule, base string) (string, []any) {
	conds := []string{base}
	var args []any
	if k := r.Scope["kind"]; k != "" {
		args = append(args, k)
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if q := r.Scope["queue"]; q != "" {
		args = append(args, q)
		conds = append(conds, fmt.Sprintf("queue = $%d", len(args)))
	}
	return " where " + strings.Join(conds, " and "), args
}

// parseBigintArray parses a Postgres bigint[] text literal '{n1,n2,...}' into []int64.
func parseBigintArray(s string) []int64 {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s == "{}" {
		return nil
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	var out []int64
	for _, tok := range strings.Split(s, ",") {
		out = append(out, i64Str(strings.TrimSpace(tok)))
	}
	return out
}

func i64Str(s string) int64 {
	var n int64
	neg := false
	for i, ch := range s {
		if ch == '-' && i == 0 {
			neg = true
			continue
		}
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int64(ch-'0')
	}
	if neg {
		return -n
	}
	return n
}
