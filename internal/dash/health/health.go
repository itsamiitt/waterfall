// Package health is the dashboard's Provider Health Center (doc 04 §2.5, doc 10 §3, master
// spec §7). It owns the scheduled per-Provider health probes, the raw->daily rollup fold, the
// uptime/latency timeline, the regional roll-up, and the auto-reactivation probes that drive
// exhausted/rate_limited Provider Keys back toward active.
//
// Boundaries (P4 build): this package touches ONLY its own tables — provider_health_checks and
// provider_health_1d (migration 0009, Class P platform_only RLS) and health_schedules
// (migration 0005). It reaches every other subsystem through an INJECTED interface, never a
// direct import of a sibling dash feature:
//   - the probe reuses internal/provider (HTTPAdapter + Call + Breaker + egress AuthInjector)
//     with a caller-supplied provider.KeyResolver; a nil resolver yields a typed no-key result,
//     never a crash;
//   - auto re-enable delegates to an injected KeyReactivator (implemented by the orchestrator
//     over internal/dash/rotation's KM-3 state machine) — health never imports rotation;
//   - persistence goes through db.Store.PlatformTx (the Class-P system path), so the scheduler
//     and reactivator run with no request Principal.
//
// Gates: G1/Class-P (platform_only tables reached only via PlatformTx); G3 (every probe is a
// bounded, timed, circuit-broken provider.Call); bounded queries (windows clamped, retention
// enforced). No secret or PII is ever logged or stored — checks record status/latency/error
// class only.
package health

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// Check status vocabulary. up/degraded/down are the provider_health_checks.status CHECK values;
// no_data is a TIMELINE-ONLY synthetic bucket label (never stored) meaning "zero checks landed in
// this bucket" — acceptance #4: a bucket with no checks renders no_data, never up.
const (
	StatusUp       = "up"
	StatusDegraded = "degraded"
	StatusDown     = "down"
	StatusNoData   = "no_data"
)

const (
	// defaultPoolName mirrors providers/rotation: the per-Provider default Key Pool selector suffix.
	defaultPoolName = "default"
	// defaultProbeTimeout bounds a single health probe when the Provider has no timeout_ms.
	defaultProbeTimeout = 5 * time.Second

	// maxDayBuckets caps the day-granularity timeline (doc 04: 90-day bars).
	maxDayBuckets = 90
	// maxHourBuckets caps the hour-granularity timeline (last 48h from raw checks).
	maxHourBuckets = 48
	// maxWindowDays bounds any timeline/stats window; beyond it the HTTP layer returns 400 (a
	// request past raw retention can never be served from raw checks — doc 04 bounded windows).
	maxWindowDays = 400
	// sampleCap bounds the raw-latency sample pulled for percentile math (keeps the read bounded).
	sampleCap = 100000
)

// Injected-interface errors.
var (
	// ErrValidation is returned for a malformed schedule or check request (HTTP 422).
	ErrValidation = errors.New("health: validation failed")
	// ErrNotFound is returned when a targeted Provider has no catalog row (HTTP 404).
	ErrNotFound = errors.New("health: provider not found")
)

// CheckResult is one probe outcome — the row written to provider_health_checks. It NEVER carries
// secret material; ErrorClass is the 8-class taxonomy string or "" on success.
type CheckResult struct {
	Status     string // up | degraded | down
	HTTPStatus int    // 0 when the transport never returned a status
	LatencyMS  int
	ErrorClass string // domain.ErrorClass.String() or "" when up
	KeyID      string // resolved Provider Key id (uuid) or "" — attribution only
	Region     string // region probed, or ""
}

// Target is the connection descriptor the scheduler/probe needs for one Provider, joined from the
// providers catalog row and its health_schedules row (defaults when the schedule row is absent).
type Target struct {
	ProviderID       string
	BaseURL          string
	AuthScheme       string
	AuthHeader       string
	AuthQueryParam   string
	TimeoutMS        int
	BreakerThreshold int
	BreakerCooldownS int
	Regions          []string
	IntervalS        int
	JitterPct        int
	Enabled          bool
}

// Schedule is a health_schedules row (per-Provider probe cadence). A missing row means defaults
// (interval 60s, jitter 10%, enabled) — the scheduler never reads providers.attrs (presentation-
// only). UpdatedBy is the acting operator's user id, or "".
type Schedule struct {
	ProviderID string    `json:"provider_id"`
	IntervalS  int       `json:"interval_s"`
	JitterPct  int       `json:"jitter_pct"`
	Regions    []string  `json:"regions,omitempty"`
	Enabled    bool      `json:"enabled"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	UpdatedBy  string    `json:"-"`
}

// validate bounds a schedule write (closed ranges; doc 10 §3.3 default 60s cadence).
func (s Schedule) validate() (string, bool) {
	if s.ProviderID == "" {
		return "provider_id is required", false
	}
	if s.IntervalS < 5 || s.IntervalS > 86400 {
		return "interval_s must be between 5 and 86400", false
	}
	if s.JitterPct < 0 || s.JitterPct > 100 {
		return "jitter_pct must be between 0 and 100", false
	}
	return "", true
}

// DayRow is one folded provider_health_1d row.
type DayRow struct {
	Day             time.Time
	Checks          int64
	OK              int64
	Degraded        int64
	Down            int64
	LatSumMS        int64
	WorstErrorClass string
}

// HourAgg is one hour's worth of raw provider_health_checks, aggregated on read for the 48h view.
type HourAgg struct {
	Hour     time.Time
	Checks   int64
	OK       int64
	Degraded int64
	Down     int64
	LatSumMS int64
}

// Bucket is one timeline cell (a day or an hour). Status is the derived bar label; a zero-check
// bucket is StatusNoData (acceptance #4). UptimePct/AvgLatMS are 0 for no_data buckets.
type Bucket struct {
	Start           time.Time `json:"start"`
	Status          string    `json:"status"`
	Checks          int64     `json:"checks"`
	UptimePct       float64   `json:"uptime_pct"`
	AvgLatMS        float64   `json:"avg_lat_ms"`
	WorstErrorClass string    `json:"worst_error_class,omitempty"`
}

// Stats is the window summary (uptime + latency percentiles) for a Provider (doc 04 item 3).
type Stats struct {
	Checks    int64   `json:"checks"`
	UptimePct float64 `json:"uptime_pct"`
	AvgLatMS  float64 `json:"avg_lat_ms"`
	P95MS     int     `json:"p95_ms"`
	P99MS     int     `json:"p99_ms"`
}

// WindowSample is the raw material for Stats: latencies (ascending) plus outcome counts over a
// bounded window. Percentiles are computed in Go from Lats (pure, unit-tested).
type WindowSample struct {
	Lats   []int
	Checks int64
	OK     int64
}

// ProviderStatus is one row of GET /health/providers: the latest observed status plus 24h uptime.
type ProviderStatus struct {
	ProviderID   string     `json:"provider_id"`
	Status       string     `json:"status"`
	LastCheckAt  *time.Time `json:"last_check_at"`
	LastLatMS    int        `json:"last_lat_ms"`
	Checks24h    int64      `json:"checks_24h"`
	UptimePct24h float64    `json:"uptime_pct_24h"`
	ErrorClass   string     `json:"error_class,omitempty"`
}

// RegionAgg is one row of GET /health/regional (aggregated over the request window).
type RegionAgg struct {
	Region    string  `json:"region"`
	Checks    int64   `json:"checks"`
	OK        int64   `json:"ok"`
	Degraded  int64   `json:"degraded"`
	Down      int64   `json:"down"`
	UptimePct float64 `json:"uptime_pct"`
}

// Store is the persistence seam (consumer-side; satisfied by *PGStore over the Class-P tables,
// and by fakes in tests). Every method runs under db.Store.PlatformTx — no request Principal.
type Store interface {
	WriteCheck(ctx context.Context, providerID string, r CheckResult, at time.Time) error
	FoldDay(ctx context.Context, dayUTC time.Time) (int, error)

	DayBuckets(ctx context.Context, providerID string, fromDay, toDay time.Time) (map[string]DayRow, error)
	HourBuckets(ctx context.Context, providerID string, from, to time.Time) (map[int64]HourAgg, error)
	SampleWindow(ctx context.Context, providerID string, from, to time.Time) (WindowSample, error)

	ProviderStatuses(ctx context.Context) ([]ProviderStatus, error)
	Regional(ctx context.Context, from, to time.Time) ([]RegionAgg, error)

	ListCheckTargets(ctx context.Context) ([]Target, error)
	ProviderTarget(ctx context.Context, providerID string) (Target, bool, error)

	ListSchedules(ctx context.Context) ([]Schedule, error)
	UpsertSchedule(ctx context.Context, s Schedule) (Schedule, error)

	ExhaustedKeys(ctx context.Context, limit int) ([]string, error)
}

// CheckFunc performs one bounded probe against a Target and returns its classified result. The
// production implementation (NewProbeCheck) reuses internal/provider; tests inject a fake so no
// probe ever hits the network.
type CheckFunc func(ctx context.Context, t Target) CheckResult

// --- pure math (unit-tested independently of Postgres) ---

// uptimePct is ok/checks as a percentage; a zero-check window is 0 (never divides by zero).
func uptimePct(ok, checks int64) float64 {
	if checks <= 0 {
		return 0
	}
	return float64(ok) / float64(checks) * 100
}

// percentileInt returns the q-quantile (0..1) of an ASCENDING-sorted slice using the
// nearest-rank method. Empty => 0. q is clamped to [0,1].
func percentileInt(sortedAsc []int, q float64) int {
	n := len(sortedAsc)
	if n == 0 {
		return 0
	}
	if q <= 0 {
		return sortedAsc[0]
	}
	if q >= 1 {
		return sortedAsc[n-1]
	}
	// nearest-rank: rank = ceil(q*n), 1-based.
	rank := int(float64(n)*q + 0.9999999)
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sortedAsc[rank-1]
}

// meanInt is the arithmetic mean of a slice, or 0 when empty.
func meanInt(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int64
	for _, x := range xs {
		sum += int64(x)
	}
	return float64(sum) / float64(len(xs))
}

// statsFrom builds a window Stats summary from a raw sample using the pure helpers above. The
// caller passes a sample whose Lats are already ascending (the store SELECTs ORDER BY lat_ms).
func statsFrom(s WindowSample) Stats {
	if !sort.IntsAreSorted(s.Lats) {
		sort.Ints(s.Lats) // defensive: fakes may hand back unsorted samples
	}
	return Stats{
		Checks:    s.Checks,
		UptimePct: uptimePct(s.OK, s.Checks),
		AvgLatMS:  meanInt(s.Lats),
		P95MS:     percentileInt(s.Lats, 0.95),
		P99MS:     percentileInt(s.Lats, 0.99),
	}
}

// statusForErr maps a probe error onto the (status, error_class) pair stored in a check row. A
// nil error is up; reachable-but-soft failures are degraded; hard failures are down. This is the
// only place the 8-class taxonomy collapses to the 3-state health vocabulary.
func statusForErr(err error) (status, errorClass string) {
	if err == nil {
		return StatusUp, ""
	}
	class := domain.ClassOf(err)
	switch class {
	case domain.ClassRateLimit, domain.ClassTransient, domain.ClassNotFound:
		return StatusDegraded, class.String()
	default: // Auth, Quota, BadRequest, ProviderDown, Unknown
		return StatusDown, class.String()
	}
}

// dayBucketStatus derives a day bar's label. ZERO checks => no_data (never up); otherwise the bar
// reflects the day's uptime band. This is the acceptance-#4 rule, isolated for direct unit test.
func dayBucketStatus(r DayRow) string {
	if r.Checks <= 0 {
		return StatusNoData
	}
	return bandForUptime(uptimePct(r.OK, r.Checks))
}

// hourBucketStatus derives an hour bar's label with the same zero-check => no_data rule.
func hourBucketStatus(a HourAgg) string {
	if a.Checks <= 0 {
		return StatusNoData
	}
	return bandForUptime(uptimePct(a.OK, a.Checks))
}

// bandForUptime turns an uptime percentage (0..100) into a bar label. Near-perfect uptime is up;
// half or more of checks failing is down; anything between is degraded. A no_data bucket is decided
// upstream (zero checks), never here — so this never returns no_data.
func bandForUptime(pct float64) string {
	switch {
	case pct >= 99.0:
		return StatusUp
	case pct < 50.0:
		return StatusDown
	default:
		return StatusDegraded
	}
}

// firstRegion returns the first configured region or "".
func firstRegion(regions []string) string {
	if len(regions) > 0 {
		return regions[0]
	}
	return ""
}
