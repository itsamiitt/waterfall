package telemetry

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
)

// partUnit is a partition period.
type partUnit int

const (
	daily partUnit = iota
	weekly
	monthly
)

// partSpec describes one partitioned parent's maintenance policy (doc 03 §4 matrix). col is the
// range key; isDate is true for date-typed keys (bounds are 'YYYY-MM-DD'), false for timestamptz.
type partSpec struct {
	parent    string
	col       string
	unit      partUnit
	retention time.Duration
	isDate    bool
}

const day = 24 * time.Hour

// partitionMatrix mirrors the doc 03 §4 partitioning & retention table exactly. This is the
// runtime maintainer's authority — migrations create only parents + DEFAULT backstops.
var partitionMatrix = []partSpec{
	{"usage_events", "created_at", daily, 48 * time.Hour, false},
	{"provider_stats_1m", "bucket_start", weekly, 7 * day, false},
	{"provider_stats_1h", "bucket_start", monthly, 90 * day, false},
	{"provider_stats_1d", "bucket_start", monthly, 730 * day, false},
	{"key_usage_1m", "bucket_start", weekly, 3 * day, false},
	{"key_usage_1h", "bucket_start", monthly, 30 * day, false},
	{"key_usage_1d", "bucket_start", monthly, 365 * day, false},
	{"tenant_usage_1h", "bucket_start", monthly, 90 * day, false},
	{"tenant_usage_1d", "bucket_start", monthly, 730 * day, false},
	{"cost_rollup_1d", "day", monthly, 730 * day, true},
	{"queue_stats_1m", "bucket_start", weekly, 7 * day, false},
	{"queue_stats_1h", "bucket_start", monthly, 30 * day, false},
	{"worker_heartbeats", "beat_at", daily, 24 * time.Hour, false},
	{"worker_stats_5m", "bucket_start", monthly, 30 * day, false},
	{"provider_health_checks", "checked_at", weekly, 30 * day, false},
	{"provider_health_1d", "day", monthly, 730 * day, true},
}

// aheadPeriods is how many periods past the current one EnsurePartitions pre-creates (doc 03 §4:
// "pre-creates partitions two periods ahead").
const aheadPeriods = 2

// Maintainer creates partitions ahead and detaches expired ones for the telemetry parents. It
// is runtime-only (never migrations, doc 03 §4). now is injectable (acceptance #5). DDL runs
// under PlatformTx; in production the app role owns these tables (doc 03 §6), which is what
// authorizes CREATE/DETACH and the defense-in-depth ENABLE/FORCE RLS on each new partition.
type Maintainer struct {
	store   *db.Store
	now     func() time.Time
	specs   []partSpec
	mu      sync.Mutex
	lastRun time.Time
	beat    *metrics.Gauge
	dflt    *metrics.Gauge
}

// NewMaintainer builds a Maintainer over the full doc 03 §4 matrix. now may be nil (wall clock);
// reg may be nil.
func NewMaintainer(store *db.Store, now func() time.Time, reg *metrics.Registry) *Maintainer {
	if now == nil {
		now = time.Now
	}
	if reg == nil {
		reg = metrics.New()
	}
	return &Maintainer{
		store: store,
		now:   now,
		specs: partitionMatrix,
		beat:  reg.Gauge("dash_partition_maintainer_heartbeat_unixtime", "unix time of the last partition-maintainer pass"),
		dflt:  reg.Gauge("dash_partition_default_rows", "rows found in a _default backstop partition (target 0)", "parent"),
	}
}

// LastRun reports the last successful maintenance pass (dead-man's-switch).
func (m *Maintainer) LastRun() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastRun
}

// EnsurePartitions pre-creates the current and next aheadPeriods partitions for every parent
// (idempotent: CREATE TABLE IF NOT EXISTS). Each parent is maintained in its own transaction so
// one parent's failure cannot block the rest; the first error encountered is returned after all
// parents are attempted (a maintainer failure is itself alertable, doc 03 §4).
func (m *Maintainer) EnsurePartitions(ctx context.Context, now time.Time) error {
	var firstErr error
	for _, sp := range m.specs {
		if err := m.ensureSpec(ctx, sp, now.UTC()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		m.mu.Lock()
		m.lastRun = now.UTC()
		m.mu.Unlock()
		m.beat.Set(float64(now.Unix()))
	}
	return firstErr
}

func (m *Maintainer) ensureSpec(ctx context.Context, sp partSpec, now time.Time) error {
	base, _ := periodBounds(sp.unit, now)
	return m.store.PlatformTx(ctx, func(c *pg.Conn) error {
		for i := 0; i <= aheadPeriods; i++ {
			lo := addPeriods(sp.unit, base, i)
			hi := nextPeriod(sp.unit, lo)
			name := partName(sp.parent, sp.unit, lo)
			ddl := "create table if not exists " + name + " partition of " + sp.parent +
				" for values from ('" + boundLit(lo, sp.isDate) + "') to ('" + boundLit(hi, sp.isDate) + "')"
			if err := c.Exec(ddl); err != nil {
				return err
			}
			// Defense-in-depth: enforce RLS on the concrete partition too (doc 03 §4).
			if err := c.Exec("alter table " + name + " enable row level security"); err != nil {
				return err
			}
			if err := c.Exec("alter table " + name + " force row level security"); err != nil {
				return err
			}
		}
		return nil
	})
}

// DetachExpired detaches (and drops) every partition whose upper bound is at or before its
// parent's retention cutoff (now - retention). The DEFAULT backstop is never detached. Returns
// the names of the partitions removed. Assert via pg_inherits (acceptance #5).
func (m *Maintainer) DetachExpired(ctx context.Context, now time.Time) ([]string, error) {
	var removed []string
	var firstErr error
	for _, sp := range m.specs {
		names, err := m.detachSpec(ctx, sp, now.UTC())
		removed = append(removed, names...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return removed, firstErr
}

func (m *Maintainer) detachSpec(ctx context.Context, sp partSpec, now time.Time) ([]string, error) {
	cutoff := now.Add(-sp.retention)
	var removed []string
	err := m.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select c.relname, pg_get_expr(c.relpartbound, c.oid)
			   from pg_inherits i
			   join pg_class c on c.oid = i.inhrelid
			   join pg_class p on p.oid = i.inhparent
			  where p.relname = $1`, sp.parent)
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			name, bound := s(r[0]), s(r[1])
			hi, ok := parseUpperBound(bound)
			if !ok { // DEFAULT partition or unparseable — never auto-detach
				continue
			}
			if hi.After(cutoff) {
				continue // still within retention
			}
			if err := c.Exec("alter table " + sp.parent + " detach partition " + name); err != nil {
				return err
			}
			if err := c.Exec("drop table if exists " + name); err != nil {
				return err
			}
			removed = append(removed, name)
		}
		return nil
	})
	return removed, err
}

// parseUpperBound extracts the TO ('...') timestamp/date from a pg_get_expr partition bound like
// `FOR VALUES FROM ('2020-01-01 00:00:00+00') TO ('2020-01-02 00:00:00+00')`. It returns ok=false
// for a DEFAULT partition or any bound it cannot parse (fail-safe: never detach what it can't
// prove is expired).
func parseUpperBound(bound string) (time.Time, bool) {
	i := strings.LastIndex(bound, "TO ('")
	if i < 0 {
		return time.Time{}, false
	}
	rest := bound[i+len("TO ('"):]
	j := strings.Index(rest, "')")
	if j < 0 {
		return time.Time{}, false
	}
	t := parseTS(rest[:j])
	if t.IsZero() {
		return time.Time{}, false
	}
	return t, true
}

// --- period math ---

func periodBounds(unit partUnit, t time.Time) (lo, hi time.Time) {
	t = t.UTC()
	switch unit {
	case daily:
		lo = t.Truncate(day)
		hi = lo.AddDate(0, 0, 1)
	case weekly:
		lo = weekStart(t)
		hi = lo.AddDate(0, 0, 7)
	case monthly:
		y, mo, _ := t.Date()
		lo = time.Date(y, mo, 1, 0, 0, 0, 0, time.UTC)
		hi = lo.AddDate(0, 1, 0)
	}
	return lo, hi
}

func nextPeriod(unit partUnit, lo time.Time) time.Time {
	switch unit {
	case daily:
		return lo.AddDate(0, 0, 1)
	case weekly:
		return lo.AddDate(0, 0, 7)
	case monthly:
		return lo.AddDate(0, 1, 0)
	}
	return lo
}

func addPeriods(unit partUnit, lo time.Time, n int) time.Time {
	switch unit {
	case daily:
		return lo.AddDate(0, 0, n)
	case weekly:
		return lo.AddDate(0, 0, 7*n)
	case monthly:
		return lo.AddDate(0, n, 0)
	}
	return lo
}

// weekStart returns UTC midnight of the Monday of t's ISO week.
func weekStart(t time.Time) time.Time {
	d := t.UTC().Truncate(day)
	delta := (int(d.Weekday()) + 6) % 7 // days since Monday
	return d.AddDate(0, 0, -delta)
}

func partName(parent string, unit partUnit, lo time.Time) string {
	if unit == monthly {
		return parent + "_p" + lo.Format("200601")
	}
	return parent + "_p" + lo.Format("20060102")
}

func boundLit(t time.Time, isDate bool) string {
	if isDate {
		return t.Format("2006-01-02")
	}
	return t.UTC().Format("2006-01-02 15:04:05-07")
}
