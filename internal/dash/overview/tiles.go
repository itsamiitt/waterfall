// Package overview is Module 1 (doc 09 §1, doc 04 §2.13): the leader-elected 2s tile
// aggregator over the P4–P6 rollups, the GET /v1/admin/overview snapshot + per-tile deep-link
// endpoints, the bounded cross-entity GET /v1/admin/search, and GET /v1/admin/meta/enums (the
// closed vocabularies served from code constants for UI parity).
//
// The 19-tile vocabulary below is NORMATIVE per doc 09 §1.2 (OI-WF-2). Tiles are computed once
// per 2s tick on the advisory-lock leader, persisted to the self_monitor 'overview_snapshot'
// row (doc 03 §2.7), served from the in-memory last tick on the leader and from the persisted
// row on followers, and fanned out as overview.tiles.tick through the realtime poller
// (ADR-0019: DB read rate O(instances), never O(clients)).
package overview

import (
	"context"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// TileIDs is the normative 19-tile vocabulary in doc 09 §1.2 order.
var TileIDs = []string{
	"providers_summary", "provider_health_split", "keys_summary", "credits_remaining",
	"requests_today", "rps_now", "jobs_summary", "retry_depth", "dlq_depth",
	"worker_health", "queue_health", "success_failure_rate", "avg_cost_per_result",
	"avg_response_ms", "provider_ranking", "coverage", "cost_today", "cost_month",
	"system_health",
}

// Drill is a tile's deep-link target (doc 09 §1.2 tile ↔ endpoint map).
type Drill struct {
	Route    string `json:"route"`
	Endpoint string `json:"endpoint"`
}

// Drills maps every tile to its drill-down (system_health deliberately has none — the single
// non-navigating tile, doc 09 §1.3).
var Drills = map[string]Drill{
	"providers_summary":     {"/providers", "GET /v1/admin/providers?op_state="},
	"provider_health_split": {"/health", "GET /v1/admin/health/providers"},
	"keys_summary":          {"/keys", "GET /v1/admin/providers/{id}/keys?status="},
	"credits_remaining":     {"/providers", "GET /v1/admin/providers?sort=credits_remaining"},
	"requests_today":        {"/cost", "GET /v1/admin/cost/summary?group_by=provider"},
	"rps_now":               {"/health", "GET /v1/admin/health/providers"},
	"jobs_summary":          {"/queues", "GET /v1/admin/queues"},
	"retry_depth":           {"/queues", "GET /v1/admin/queues"},
	"dlq_depth":             {"/dead-letters", "GET /v1/admin/dead-letters"},
	"worker_health":         {"/workers", "GET /v1/admin/workers"},
	"queue_health":          {"/queues/:name", "GET /v1/admin/queues/{name}/stats"},
	"success_failure_rate":  {"/health/:providerId", "GET /v1/admin/providers/{id}/stats"},
	"avg_cost_per_result":   {"/cost", "GET /v1/admin/cost/per-enrichment"},
	"avg_response_ms":       {"/health/:providerId", "GET /v1/admin/providers/{id}/stats"},
	"provider_ranking":      {"/providers/compare", "GET /v1/admin/providers/rankings"},
	"coverage":              {"/providers/compare", "GET /v1/admin/providers/coverage"},
	"cost_today":            {"/cost", "GET /v1/admin/cost/summary?group_by=provider&from=&to="},
	"cost_month":            {"/cost", "GET /v1/admin/cost/summary"},
	"system_health":         {},
}

// ValidTile reports tile-id membership.
func ValidTile(id string) bool { _, ok := Drills[id]; return ok }

// Tiles is the computed snapshot. Field order mirrors doc 09 §1.2. All values are platform
// aggregates (v1 operator-scoped overview, doc 12 §P7) — ids and counts only, never PII.
type Tiles struct {
	ProvidersSummary struct {
		Total    int64 `json:"total"`
		Active   int64 `json:"active"`
		Disabled int64 `json:"disabled"`
	} `json:"providers_summary"`
	ProviderHealthSplit struct {
		Healthy  int64 `json:"healthy"`
		Degraded int64 `json:"degraded"`
		Offline  int64 `json:"offline"` // down + never-checked
	} `json:"provider_health_split"`
	KeysSummary struct {
		Total   int64 `json:"total"`
		Active  int64 `json:"active"`
		Failed  int64 `json:"failed"` // status=auth_failed (doc 09 drill /keys?status=auth_failed)
		Expired int64 `json:"expired"`
	} `json:"keys_summary"`
	CreditsRemaining struct {
		Value   int64  `json:"value"`
		Source  string `json:"source"` // always "modeled" (doc 09)
		Percent *int64 `json:"budget_pct,omitempty"`
	} `json:"credits_remaining"`
	RequestsToday struct {
		Value    int64    `json:"value"`
		DeltaPct *float64 `json:"delta_pct,omitempty"` // vs yesterday; omitted when no baseline
	} `json:"requests_today"`
	RPSNow struct {
		Value float64 `json:"value"` // latest provider_stats_1m bucket / 60
	} `json:"rps_now"`
	JobsSummary struct {
		Running int64 `json:"running"`
		Queued  int64 `json:"queued"`
		Failed  int64 `json:"failed"`
	} `json:"jobs_summary"`
	RetryDepth struct {
		Value int64 `json:"value"`
	} `json:"retry_depth"`
	DLQDepth struct {
		Value int64 `json:"value"`
	} `json:"dlq_depth"`
	WorkerHealth struct {
		Running  int64 `json:"running"`
		Total    int64 `json:"total"`
		Lost     int64 `json:"lost"`
		Draining int64 `json:"draining"`
	} `json:"worker_health"`
	QueueHealth struct {
		Queue  string `json:"queue"`
		ValueS int64  `json:"value_s"` // worst oldest_age_s
	} `json:"queue_health"`
	SuccessFailureRate struct {
		SuccessPct *float64 `json:"success_pct,omitempty"` // 1h window; omitted when no data
		FailurePct *float64 `json:"failure_pct,omitempty"`
		WindowS    int      `json:"window_s"`
	} `json:"success_failure_rate"`
	AvgCostPerResult struct {
		CreditsPerSuccess *float64 `json:"credits_per_success,omitempty"`
		CreditsPerCall    *float64 `json:"credits_per_call,omitempty"`
	} `json:"avg_cost_per_result"`
	AvgResponseMs struct {
		P50 *int64 `json:"p50,omitempty"`
		P95 *int64 `json:"p95,omitempty"`
	} `json:"avg_response_ms"`
	ProviderRanking []RankEntry `json:"provider_ranking"` // top providers by measured cost-per-hit
	Coverage        struct {
		OverallPct      *float64 `json:"overall_pct,omitempty"`
		WorkEmailPct    *float64 `json:"work_email_pct,omitempty"`
		MobileDirectPct *float64 `json:"mobile_direct_pct,omitempty"`
		IntentPct       *float64 `json:"intent_pct,omitempty"`
	} `json:"coverage"`
	CostToday struct {
		Credits   int64  `json:"credits"`
		BudgetPct *int64 `json:"budget_pct,omitempty"`
		Source    string `json:"source"` // "modeled"
	} `json:"cost_today"`
	CostMonth struct {
		Credits int64  `json:"credits"`
		Source  string `json:"source"` // "modeled"
	} `json:"cost_month"`
	SystemHealth struct {
		Status          string   `json:"status"` // ok | degraded
		AggregatorAgeS  *float64 `json:"aggregator_age_s,omitempty"`
		OverviewAgeS    *float64 `json:"overview_age_s,omitempty"`
		QueueSamplerAge *float64 `json:"queue_sampler_age_s,omitempty"`
	} `json:"system_health"`
}

// RankEntry is one provider_ranking row: measured credits per successful hit.
type RankEntry struct {
	Rank          int     `json:"rank"`
	ProviderID    string  `json:"provider_id"`
	CreditsPerHit float64 `json:"credits_per_hit"`
}

// latBucketsMs mirrors the telemetry histogram boundaries (doc 03 §2.6 single source of
// truth; kept in lockstep with internal/dash/telemetry and internal/dash/alerts).
var latBucketsMs = [20]int64{
	1, 2, 4, 8, 16, 32, 64, 125, 250, 500,
	1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 1<<62 - 1,
}

// ComputeTiles computes the 19 tiles from each tile's documented source (doc 09 §1.2) under
// ONE platform transaction. Every read is a bounded aggregate over rollups or registry tables
// — never raw usage_events (doc 10 §2). heartbeats carries self_monitor freshness ages for the
// system_health tile (nil on the inline-compute path).
func ComputeTiles(c *pg.Conn, now time.Time, heartbeats map[string]float64) (Tiles, error) {
	var t Tiles
	now = now.UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	hourAgo := now.Add(-time.Hour)

	// providers_summary + credits_remaining (source: providers registry).
	res, err := c.Query(`select count(*),
		count(*) filter (where op_state = 'enabled'),
		count(*) filter (where op_state = 'disabled'),
		coalesce(sum(credits_remaining), 0)
		from providers where archived_at is null`)
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		t.ProvidersSummary.Total = i64(r[0])
		t.ProvidersSummary.Active = i64(r[1])
		t.ProvidersSummary.Disabled = i64(r[2])
		t.CreditsRemaining.Value = i64(r[3])
	}
	t.CreditsRemaining.Source = "modeled"

	// provider_health_split (source: latest provider_health_checks status per provider;
	// never-checked providers count as offline so the split always sums to total).
	res, err = c.Query(`with latest as (
		select distinct on (provider_id) provider_id, status
		from provider_health_checks order by provider_id, checked_at desc)
		select count(*) filter (where l.status = 'up'),
		       count(*) filter (where l.status = 'degraded'),
		       count(*) filter (where l.status = 'down' or l.status is null)
		from providers p left join latest l on l.provider_id = p.id
		where p.archived_at is null`)
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		t.ProviderHealthSplit.Healthy = i64(r[0])
		t.ProviderHealthSplit.Degraded = i64(r[1])
		t.ProviderHealthSplit.Offline = i64(r[2])
	}

	// keys_summary (source: provider_keys registry by status).
	res, err = c.Query(`select count(*),
		count(*) filter (where status = 'active'),
		count(*) filter (where status = 'auth_failed'),
		count(*) filter (where status = 'expired')
		from provider_keys`)
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		t.KeysSummary.Total = i64(r[0])
		t.KeysSummary.Active = i64(r[1])
		t.KeysSummary.Failed = i64(r[2])
		t.KeysSummary.Expired = i64(r[3])
	}

	// requests_today + delta vs yesterday (source: provider_stats_1d buckets).
	res, err = c.QueryParams(`select
		coalesce(sum(req) filter (where bucket_start = $1), 0),
		coalesce(sum(req) filter (where bucket_start = $2), 0)
		from provider_stats_1d where bucket_start >= $2`,
		dayStart, dayStart.AddDate(0, 0, -1))
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		today, yesterday := i64(r[0]), i64(r[1])
		t.RequestsToday.Value = today
		if yesterday > 0 {
			d := (float64(today) - float64(yesterday)) / float64(yesterday) * 100
			t.RequestsToday.DeltaPct = &d
		}
	}

	// rps_now (source: the latest provider_stats_1m bucket across providers).
	res, err = c.Query(`select coalesce(sum(req), 0) from provider_stats_1m
		where bucket_start = (select max(bucket_start) from provider_stats_1m)`)
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		t.RPSNow.Value = float64(i64(r[0])) / 60.0
	}

	// jobs_summary / retry_depth / dlq_depth / queue_health (source: newest queue_stats_1m
	// bucket per queue — the fold's durable output, never a per-request COUNT(*), OI-QW-9).
	res, err = c.Query(`select distinct on (queue) queue, depth, running, retry, failed, dead,
		oldest_age_s from queue_stats_1m order by queue, bucket_start desc`)
	if err != nil {
		return t, err
	}
	for _, r := range res.Rows {
		depth, running, retry := i64(r[1]), i64(r[2]), i64(r[3])
		t.JobsSummary.Running += running
		if q := depth - running - retry; q > 0 {
			t.JobsSummary.Queued += q
		}
		t.JobsSummary.Failed += i64(r[4])
		t.RetryDepth.Value += retry
		t.DLQDepth.Value += i64(r[5])
		if age := i64(r[6]); t.QueueHealth.Queue == "" || age > t.QueueHealth.ValueS {
			t.QueueHealth.Queue, t.QueueHealth.ValueS = str(r[0]), age
		}
	}

	// worker_health (source: workers registry).
	res, err = c.Query(`select count(*) filter (where status = 'running'), count(*),
		count(*) filter (where status = 'lost'),
		count(*) filter (where status = 'draining') from workers`)
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		t.WorkerHealth.Running = i64(r[0])
		t.WorkerHealth.Total = i64(r[1])
		t.WorkerHealth.Lost = i64(r[2])
		t.WorkerHealth.Draining = i64(r[3])
	}

	// success_failure_rate, 1h window (source: provider_stats_1m sums).
	res, err = c.QueryParams(`select coalesce(sum(req),0), coalesce(sum(ok),0)
		from provider_stats_1m where bucket_start >= $1`, hourAgo)
	if err != nil {
		return t, err
	}
	t.SuccessFailureRate.WindowS = 3600
	if r := row(res); r != nil {
		if req := i64(r[0]); req > 0 {
			okPct := float64(i64(r[1])) / float64(req) * 100
			failPct := 100 - okPct
			t.SuccessFailureRate.SuccessPct = &okPct
			t.SuccessFailureRate.FailurePct = &failPct
		}
	}

	// avg_cost_per_result + cost_today + cost_month (source: cost_rollup_1d, cross-tenant via
	// the enumerated operator SELECT policy; figures are modeled, doc 09 §10).
	res, err = c.QueryParams(`select coalesce(sum(credits),0), coalesce(sum(calls),0),
		coalesce(sum(successful_results),0) from cost_rollup_1d where day = $1`,
		dayStart.Format("2006-01-02"))
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		credits, calls, successes := i64(r[0]), i64(r[1]), i64(r[2])
		t.CostToday.Credits = credits
		if successes > 0 {
			v := float64(credits) / float64(successes)
			t.AvgCostPerResult.CreditsPerSuccess = &v
		}
		if calls > 0 {
			v := float64(credits) / float64(calls)
			t.AvgCostPerResult.CreditsPerCall = &v
		}
	}
	t.CostToday.Source = "modeled"
	res, err = c.QueryParams(`select coalesce(sum(credits),0) from cost_rollup_1d
		where day >= $1`, monthStart.Format("2006-01-02"))
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		t.CostMonth.Credits = i64(r[0])
	}
	t.CostMonth.Source = "modeled"

	// cost_today budget % (source: budgets — platform Tenant rows only under this tx; the
	// per-Tenant budget views live in /cost, doc 09 §10).
	res, err = c.Query(`select limit_credits from budgets
		where scope = 'tenant' and period = 'day' order by scope_key limit 1`)
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		if limit := i64(r[0]); limit > 0 {
			pct := t.CostToday.Credits * 100 / limit
			t.CostToday.BudgetPct = &pct
		}
	}

	// avg_response_ms p50/p95 (source: provider_stats_1h lat_hist over the last 2 hourly
	// buckets — bounded to <= 2 rows per provider; percentiles computed at read, doc 03 §2.6).
	res, err = c.QueryParams(`select lat_hist from provider_stats_1h
		where bucket_start >= $1`, now.Add(-2*time.Hour).Truncate(time.Hour))
	if err != nil {
		return t, err
	}
	var hist [20]int64
	var histTotal int64
	for _, r := range res.Rows {
		for i, v := range parseBigintArray(str(r[0])) {
			if i < 20 {
				hist[i] += v
				histTotal += v
			}
		}
	}
	if histTotal > 0 {
		p50 := percentile(&hist, histTotal, 0.50)
		p95 := percentile(&hist, histTotal, 0.95)
		t.AvgResponseMs.P50 = &p50
		t.AvgResponseMs.P95 = &p95
	}

	// provider_ranking (source: today's provider_stats_1d — measured credits per hit, cheapest
	// first, top 3; providers with no successes are unranked).
	res, err = c.QueryParams(`select provider_id,
		credits_spent::float8 / nullif(ok, 0) as cph
		from provider_stats_1d where bucket_start = $1 and ok > 0
		order by cph asc nulls last limit 3`, dayStart)
	if err != nil {
		return t, err
	}
	t.ProviderRanking = []RankEntry{}
	for i, r := range res.Rows {
		t.ProviderRanking = append(t.ProviderRanking, RankEntry{
			Rank: i + 1, ProviderID: str(r[0]), CreditsPerHit: f64(r[1]),
		})
	}

	// coverage (source: declared capabilities over the non-archived catalog, mirroring
	// GET /providers/coverage semantics; doc 09 field trio incl. intent_topics).
	res, err = c.Query(`select count(*),
		count(*) filter (where jsonb_typeof(capabilities) = 'array' and jsonb_array_length(capabilities) > 0),
		count(*) filter (where capabilities @> '[{"field":"work_email"}]'),
		count(*) filter (where capabilities @> '[{"field":"mobile_phone"}]'
		              or capabilities @> '[{"field":"direct_dial"}]'),
		count(*) filter (where capabilities @> '[{"field":"intent_topics"}]')
		from providers where archived_at is null`)
	if err != nil {
		return t, err
	}
	if r := row(res); r != nil {
		if total := i64(r[0]); total > 0 {
			t.Coverage.OverallPct = pctPtr(i64(r[1]), total)
			t.Coverage.WorkEmailPct = pctPtr(i64(r[2]), total)
			t.Coverage.MobileDirectPct = pctPtr(i64(r[3]), total)
			t.Coverage.IntentPct = pctPtr(i64(r[4]), total)
		}
	}

	// system_health (source: self_monitor heartbeat freshness, doc 10 self-monitoring).
	t.SystemHealth.Status = "ok"
	if age, ok := heartbeats["fold:usage"]; ok {
		t.SystemHealth.AggregatorAgeS = ptr(age)
		if age > 30 {
			t.SystemHealth.Status = "degraded"
		}
	}
	if age, ok := heartbeats["overview_snapshot"]; ok {
		t.SystemHealth.OverviewAgeS = ptr(age)
		if age > 30 {
			t.SystemHealth.Status = "degraded"
		}
	}
	if age, ok := heartbeats["queue_stats_sample"]; ok {
		t.SystemHealth.QueueSamplerAge = ptr(age)
		if age > 60 {
			t.SystemHealth.Status = "degraded"
		}
	}
	return t, nil
}

// ComputeTilesTx runs ComputeTiles under one PlatformTx (the inline-compute path for a fresh
// boot with no persisted snapshot yet, and the leader's per-tick path).
func ComputeTilesTx(ctx context.Context, store *db.Store, now time.Time, heartbeats map[string]float64) (Tiles, error) {
	var t Tiles
	err := store.PlatformTx(ctx, func(c *pg.Conn) error {
		var cerr error
		t, cerr = ComputeTiles(c, now, heartbeats)
		return cerr
	})
	return t, err
}

// percentile returns the histogram bucket upper bound covering quantile q.
func percentile(hist *[20]int64, total int64, q float64) int64 {
	target := int64(float64(total)*q + 0.999999)
	if target < 1 {
		target = 1
	}
	var cum int64
	for i, cnt := range hist {
		cum += cnt
		if cum >= target {
			return latBucketsMs[i]
		}
	}
	return latBucketsMs[19]
}

func pctPtr(n, total int64) *float64 {
	v := float64(n) / float64(total) * 100
	return &v
}

func ptr(v float64) *float64 { return &v }

func row(res *pg.Result) []*string {
	if len(res.Rows) == 0 {
		return nil
	}
	return res.Rows[0]
}
