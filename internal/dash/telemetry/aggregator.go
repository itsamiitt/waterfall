package telemetry

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// mergeMode selects how a fold reconciles a computed bucket with any existing rollup row.
type mergeMode int

const (
	// modeAdd is the incremental fold (doc 03 §9.4): additive upsert `x = x + EXCLUDED.x`.
	// Correct because the watermark guarantees each usage_events row is folded exactly once.
	modeAdd mergeMode = iota
	// modeReplace is the repair refold: it recomputes WHOLE buckets from usage_events and
	// replaces (`x = EXCLUDED.x`). Deterministic and idempotent — re-running over the same
	// events reproduces byte-identical rollups (P4 acceptance #1). Windows MUST be
	// bucket-aligned so every touched bucket is fully covered.
	modeReplace
)

// defaultLookback bounds a cold-start incremental fold to the usage_events retention window
// (doc 03 §4: 48h); a fresh leader never scans beyond replayable raw data.
const defaultLookback = 48 * time.Hour

// Aggregator is the leader-elected fold engine (doc 10 §2). It reads unfolded usage_events past
// an in-memory watermark and folds every usage-derived rollup family: provider_stats_{1m,1h,1d}
// and key_usage_{1m,1h,1d} (Class P, written under 'platform') and tenant_usage_{1h,1d} +
// cost_rollup_1d (Class T, written inside each Tenant's transaction). Single writer — only the
// leader calls Fold (doc 03 §6).
//
// Fold idempotency mechanism (documented for the P4 gate): folds compute complete per-bucket
// aggregates from usage_events and upsert. The incremental Fold advances a watermark so each
// raw row is counted exactly once and merges additively; the repair Refold recomputes whole
// buckets and REPLACES. Because Refold derives every column deterministically from the raw
// events and the rollup tables carry no write-time columns (no now()/serial), two refolds of
// the same events with a truncate between are byte-identical.
type Aggregator struct {
	store    *db.Store
	now      func() time.Time
	lookback time.Duration

	mu        sync.Mutex
	watermark time.Time
	lastFold  time.Time

	// metrics (bounded label sets, doc 10 §1)
	foldDur *metrics.Histogram
	lag     *metrics.Gauge
	beat    *metrics.Gauge
}

// NewAggregator builds an Aggregator. now may be nil (wall clock); reg may be nil (private
// registry). The watermark starts unset — the first incremental Fold seeds it to now-lookback.
func NewAggregator(store *db.Store, now func() time.Time, reg *metrics.Registry) *Aggregator {
	if now == nil {
		now = time.Now
	}
	if reg == nil {
		reg = metrics.New()
	}
	return &Aggregator{
		store:    store,
		now:      now,
		lookback: defaultLookback,
		foldDur:  reg.Histogram("dash_aggregator_fold_duration_seconds", "fold wall time per cycle", []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}, "fold"),
		lag:      reg.Gauge("dash_aggregator_lag_seconds", "now - fold watermark (rollup staleness)", "fold"),
		beat:     reg.Gauge("dash_aggregator_heartbeat_unixtime", "unix time of the aggregator's last successful fold"),
	}
}

// SeedWatermark sets the fold watermark explicitly (leader election / test setup).
func (a *Aggregator) SeedWatermark(t time.Time) {
	a.mu.Lock()
	a.watermark = t.UTC()
	a.mu.Unlock()
}

// LastFold reports the time of the last successful fold (dead-man's-switch: the health
// self-monitor treats a stale value as aggregator failure, doc 10 §6).
func (a *Aggregator) LastFold() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastFold
}

// Watermark reports the current fold watermark.
func (a *Aggregator) Watermark() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.watermark
}

// Fold runs one incremental fold: it consumes every usage_events row with created_at in
// (watermark, now] exactly once, merges additively, and advances the watermark. Returns the
// number of raw events folded.
func (a *Aggregator) Fold(ctx context.Context) (int, error) {
	to := a.now().UTC()
	a.mu.Lock()
	from := a.watermark
	if from.IsZero() {
		from = to.Add(-a.lookback)
	}
	a.mu.Unlock()
	if !to.After(from) {
		return 0, nil
	}
	start := time.Now()
	n, err := a.foldWindow(ctx, from, to, modeAdd)
	if err != nil {
		return 0, err
	}
	a.mu.Lock()
	a.watermark = to
	a.lastFold = to
	a.mu.Unlock()
	a.foldDur.Observe(time.Since(start).Seconds(), "usage")
	a.lag.Set(0, "usage")
	a.beat.Set(float64(to.Unix()))
	return n, nil
}

// Refold recomputes and REPLACES all usage-derived rollups for the bucket-aligned window
// [from, to). This is the repair path (doc 03 §9.4) and the P4 acceptance-#1 idempotency path:
// truncate rollups, Refold, and the rows are reproduced byte-for-byte. It does not move the
// incremental watermark. Returns the number of raw events refolded.
func (a *Aggregator) Refold(ctx context.Context, from, to time.Time) (int, error) {
	start := time.Now()
	n, err := a.foldWindow(ctx, from.UTC(), to.UTC(), modeReplace)
	if err != nil {
		return 0, err
	}
	a.foldDur.Observe(time.Since(start).Seconds(), "refold")
	return n, nil
}

// foldWindow is the shared fold core. It iterates the operator-readable Tenant registry and,
// per Tenant, reads that Tenant's usage_events in [from,to) under its own dual-GUC transaction
// (RLS scopes the read), flushing the Tenant's Class T rollups in that same transaction. Class P
// aggregates accumulate in memory across all Tenants and are written once under 'platform'
// (doc 03 §9.4, OI-DB-3).
func (a *Aggregator) foldWindow(ctx context.Context, from, to time.Time, mode mergeMode) (int, error) {
	tenants, err := listCustomerTenants(ctx, a.store)
	if err != nil {
		return 0, err
	}
	fs := newFoldState()
	total := 0
	for _, tid := range tenants {
		tctx := principalFor(ctx, tid)
		txErr := a.store.Tx(tctx, func(c *pg.Conn) error {
			res, qerr := c.QueryParams(
				`select tenant_id, provider_id, key_id, workflow_key, country, outcome_class,
				        credits, lat_ms, created_at
				 from usage_events where created_at >= $1 and created_at < $2`, from, to)
			if qerr != nil {
				return qerr
			}
			tsAcc := newTenantState()
			for _, r := range res.Rows {
				e := foldEvent{
					tenant:   s(r[0]),
					provider: s(r[1]),
					keyID:    s(r[2]),
					workflow: s(r[3]),
					country:  s(r[4]),
					outcome:  s(r[5]),
					credits:  i64(r[6]),
					ts:       parseTS(s(r[8])),
				}
				if r[7] != nil {
					e.latMs = i64(r[7])
					e.hasLat = true
				}
				fs.addProvider(e)
				fs.addKey(e)
				tsAcc.add(e)
			}
			total += len(res.Rows)
			return tsAcc.flush(c, mode)
		})
		if txErr != nil {
			return 0, txErr
		}
	}
	if err := a.store.PlatformTx(ctx, func(c *pg.Conn) error { return fs.flushClassP(c, mode) }); err != nil {
		return 0, err
	}
	return total, nil
}

// principalFor binds a system Principal for Tenant tid (role=operator) so a dual-GUC Tx scopes
// RLS to that Tenant. Used by the aggregator/reconciler to fold each Tenant's usage_events under
// its own transaction (doc 03 §9.4). This is a system path, not a request handler.
func principalFor(ctx context.Context, tid string) context.Context {
	return tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tid, Scopes: []string{"role:operator"}})
}

// listCustomerTenants returns every non-platform Tenant id, ordered. It reads under PlatformTx
// (role=operator), which the tenants_operator_read SELECT policy authorizes cross-Tenant. Fold
// order does not affect results (all Class P merges are commutative sums).
func listCustomerTenants(ctx context.Context, store *db.Store) ([]string, error) {
	var out []string
	err := store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query("select id from tenants where id <> 'platform' order by id")
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			if r[0] != nil {
				out = append(out, *r[0])
			}
		}
		return nil
	})
	return out, err
}

// --- in-memory fold state ---

type foldEvent struct {
	tenant, provider, keyID, workflow, country, outcome string
	credits                                             int64
	latMs                                               int64
	hasLat                                              bool
	ts                                                  time.Time
}

type provKey struct {
	res      Resolution
	provider string
	bucket   time.Time
}

type provAgg struct {
	req, ok int64
	fail    [8]int64
	timeout int64
	credits int64
	latSum  int64
	hist    [histBuckets]int64
}

func (p *provAgg) add(e foldEvent) {
	p.req++
	if e.outcome == OutcomeOK {
		p.ok++
	} else {
		p.fail[failIndex(e.outcome)]++
	}
	p.credits += e.credits
	if e.hasLat {
		p.latSum += e.latMs
		p.hist[bucketIndex(e.latMs)]++
	}
}

type keyKey struct {
	res    Resolution
	keyID  string
	bucket time.Time
}

type keyAgg struct {
	req, ok, fail, credits int64
	hist                   [histBuckets]int64
}

func (k *keyAgg) add(e foldEvent) {
	k.req++
	if e.outcome == OutcomeOK {
		k.ok++
	} else {
		k.fail++
	}
	k.credits += e.credits
	if e.hasLat {
		k.hist[bucketIndex(e.latMs)]++
	}
}

// foldState accumulates the Class P families (cross-Tenant).
type foldState struct {
	prov map[provKey]*provAgg
	key  map[keyKey]*keyAgg
}

func newFoldState() *foldState {
	return &foldState{prov: map[provKey]*provAgg{}, key: map[keyKey]*keyAgg{}}
}

var provResolutions = []Resolution{Res1m, Res1h, Res1d}

func (fs *foldState) addProvider(e foldEvent) {
	for _, res := range provResolutions {
		k := provKey{res: res, provider: e.provider, bucket: bucketStart(e.ts, res)}
		a := fs.prov[k]
		if a == nil {
			a = &provAgg{}
			fs.prov[k] = a
		}
		a.add(e)
	}
}

func (fs *foldState) addKey(e foldEvent) {
	if e.keyID == "" {
		return
	}
	for _, res := range provResolutions {
		k := keyKey{res: res, keyID: e.keyID, bucket: bucketStart(e.ts, res)}
		a := fs.key[k]
		if a == nil {
			a = &keyAgg{}
			fs.key[k] = a
		}
		a.add(e)
	}
}

func (fs *foldState) flushClassP(c *pg.Conn, mode mergeMode) error {
	for k, a := range fs.prov {
		if err := upsertProviderStats(c, "provider_stats_"+string(k.res), k, a, mode); err != nil {
			return err
		}
	}
	for k, a := range fs.key {
		if err := upsertKeyUsage(c, "key_usage_"+string(k.res), k, a, mode); err != nil {
			return err
		}
	}
	return nil
}

// tenantState accumulates the Class T families for ONE Tenant (flushed in that Tenant's tx).
type tenantState struct {
	tenant string
	tu     map[tuKey]*tuAgg
	cost   map[costKey]*costAgg
}

type tuKey struct {
	res      Resolution // 1h or 1d
	provider string
	workflow string
	bucket   time.Time
}

type tuAgg struct{ req, fieldsFilled, credits int64 }

type costKey struct {
	provider string
	workflow string
	country  string
	day      time.Time
}

type costAgg struct{ credits, calls, successful int64 }

func newTenantState() *tenantState {
	return &tenantState{tu: map[tuKey]*tuAgg{}, cost: map[costKey]*costAgg{}}
}

var tenantResolutions = []Resolution{Res1h, Res1d}

func (ts *tenantState) add(e foldEvent) {
	ts.tenant = e.tenant
	ok := int64(0)
	if e.outcome == OutcomeOK {
		ok = 1
	}
	for _, res := range tenantResolutions {
		k := tuKey{res: res, provider: e.provider, workflow: e.workflow, bucket: bucketStart(e.ts, res)}
		a := ts.tu[k]
		if a == nil {
			a = &tuAgg{}
			ts.tu[k] = a
		}
		a.req++
		a.fieldsFilled += ok
		a.credits += e.credits
	}
	ck := costKey{provider: e.provider, workflow: e.workflow, country: e.country, day: bucketStart(e.ts, Res1d)}
	ca := ts.cost[ck]
	if ca == nil {
		ca = &costAgg{}
		ts.cost[ck] = ca
	}
	ca.calls++
	ca.successful += ok
	ca.credits += e.credits
}

func (ts *tenantState) flush(c *pg.Conn, mode mergeMode) error {
	for k, a := range ts.tu {
		if err := upsertTenantUsage(c, "tenant_usage_"+string(k.res), ts.tenant, k, a, mode); err != nil {
			return err
		}
	}
	for k, a := range ts.cost {
		if err := upsertCostRollup(c, ts.tenant, k, a, mode); err != nil {
			return err
		}
	}
	return nil
}

// --- upsert SQL builders ---

// setScalars renders the ON CONFLICT DO UPDATE assignments for scalar columns: additive
// (`c = table.c + EXCLUDED.c`) or replacing (`c = EXCLUDED.c`).
func setScalars(table string, cols []string, mode mergeMode) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		if mode == modeAdd {
			parts = append(parts, fmt.Sprintf("%s = %s.%s + EXCLUDED.%s", c, table, c, c))
		} else {
			parts = append(parts, fmt.Sprintf("%s = EXCLUDED.%s", c, c))
		}
	}
	return strings.Join(parts, ", ")
}

// setHist renders the lat_hist assignment. Additive is an explicit element-wise sum over the
// fixed 20-element array (no server-side function needed); replace takes EXCLUDED wholesale.
func setHist(table string, mode mergeMode) string {
	if mode == modeReplace {
		return "lat_hist = EXCLUDED.lat_hist"
	}
	parts := make([]string, 0, histBuckets)
	for i := 1; i <= histBuckets; i++ {
		parts = append(parts, fmt.Sprintf("COALESCE(%s.lat_hist[%d],0)+EXCLUDED.lat_hist[%d]", table, i, i))
	}
	return "lat_hist = ARRAY[" + strings.Join(parts, ",") + "]::bigint[]"
}

var providerStatsScalars = []string{
	"req", "ok", "fail_auth", "fail_rate_limit", "fail_transient", "fail_not_found",
	"fail_bad_request", "fail_quota", "fail_provider_down", "fail_unknown",
	"timeout_count", "credits_spent", "lat_sum_ms",
}

func upsertProviderStats(c *pg.Conn, table string, k provKey, a *provAgg, mode mergeMode) error {
	sql := `insert into ` + table + ` (provider_id, bucket_start, req, ok,
		fail_auth, fail_rate_limit, fail_transient, fail_not_found, fail_bad_request,
		fail_quota, fail_provider_down, fail_unknown, timeout_count, credits_spent, lat_sum_ms, lat_hist)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::bigint[])
		on conflict (provider_id, bucket_start) do update set ` +
		setScalars(table, providerStatsScalars, mode) + ", " + setHist(table, mode)
	return c.ExecParams(sql, k.provider, k.bucket, a.req, a.ok,
		a.fail[0], a.fail[1], a.fail[2], a.fail[3], a.fail[4], a.fail[5], a.fail[6], a.fail[7],
		a.timeout, a.credits, a.latSum, histLiteral(&a.hist))
}

var keyUsageScalars = []string{"req", "ok", "fail", "credits_spent"}

func upsertKeyUsage(c *pg.Conn, table string, k keyKey, a *keyAgg, mode mergeMode) error {
	sql := `insert into ` + table + ` (key_id, bucket_start, req, ok, fail, credits_spent, lat_hist)
		values ($1,$2,$3,$4,$5,$6,$7::bigint[])
		on conflict (key_id, bucket_start) do update set ` +
		setScalars(table, keyUsageScalars, mode) + ", " + setHist(table, mode)
	return c.ExecParams(sql, k.keyID, k.bucket, a.req, a.ok, a.fail, a.credits, histLiteral(&a.hist))
}

var tenantUsageScalars = []string{"req", "fields_filled", "credits"}

func upsertTenantUsage(c *pg.Conn, table, tenantID string, k tuKey, a *tuAgg, mode mergeMode) error {
	sql := `insert into ` + table + ` (tenant_id, provider_id, workflow_key, bucket_start, req, fields_filled, credits)
		values ($1,$2,$3,$4,$5,$6,$7)
		on conflict (tenant_id, provider_id, workflow_key, bucket_start) do update set ` +
		setScalars(table, tenantUsageScalars, mode)
	return c.ExecParams(sql, tenantID, k.provider, k.workflow, k.bucket, a.req, a.fieldsFilled, a.credits)
}

var costRollupScalars = []string{"credits", "calls", "successful_results"}

func upsertCostRollup(c *pg.Conn, tenantID string, k costKey, a *costAgg, mode mergeMode) error {
	sql := `insert into cost_rollup_1d (tenant_id, provider_id, workflow_key, country, day, credits, calls, successful_results)
		values ($1,$2,$3,$4,$5,$6,$7,$8)
		on conflict (tenant_id, provider_id, workflow_key, country, day) do update set ` +
		setScalars("cost_rollup_1d", costRollupScalars, mode)
	return c.ExecParams(sql, tenantID, k.provider, k.workflow, k.country, k.day.Format("2006-01-02"),
		a.credits, a.calls, a.successful)
}
