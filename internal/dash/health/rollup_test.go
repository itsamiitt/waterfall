package health

import (
	"errors"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// TestBuildDaySeries_ZeroChecksIsNoDataNotUp is the acceptance-#4 unit: a day bucket with no
// checks (missing folded row, or a row whose checks==0) renders no_data — NEVER up. It also proves
// the series is contiguous and capped at 90 buckets.
func TestBuildDaySeries_ZeroChecksIsNoDataNotUp(t *testing.T) {
	rows := map[string]DayRow{
		"2026-04-01": {Checks: 10, OK: 10, LatSumMS: 500},                                          // all green -> up
		"2026-04-03": {Checks: 8, OK: 2, Down: 6, LatSumMS: 800, WorstErrorClass: "PROVIDER_DOWN"}, // down
		"2026-04-04": {Checks: 0},                                                                  // explicit zero -> no_data
	}
	from := day(2026, 4, 1)
	to := day(2026, 4, 6) // [01,06) => 5 buckets: 01,02,03,04,05

	buckets := buildDaySeries(from, to, rows)
	if len(buckets) != 5 {
		t.Fatalf("want 5 buckets, got %d", len(buckets))
	}
	want := []string{StatusUp, StatusNoData, StatusDown, StatusNoData, StatusNoData}
	for i, b := range buckets {
		if b.Status != want[i] {
			t.Errorf("bucket[%d] (%s) status=%s want %s", i, b.Start.Format("2006-01-02"), b.Status, want[i])
		}
	}
	// The zero-check days must NOT be up.
	for _, b := range buckets {
		if b.Checks == 0 && b.Status == StatusUp {
			t.Fatalf("zero-check bucket %s rendered up (must be no_data)", b.Start.Format("2006-01-02"))
		}
	}
	// up-day uptime + avg latency.
	if buckets[0].UptimePct != 100 || buckets[0].AvgLatMS != 50 {
		t.Errorf("up-day summary: uptime=%.1f avg=%.1f", buckets[0].UptimePct, buckets[0].AvgLatMS)
	}
	if buckets[2].WorstErrorClass != "PROVIDER_DOWN" {
		t.Errorf("down-day worst_error_class=%q", buckets[2].WorstErrorClass)
	}
}

// TestBuildDaySeries_CapAt90 proves a 90-day window yields exactly 90 day-buckets (all no_data when
// no rows), matching the acceptance timeline shape.
func TestBuildDaySeries_CapAt90(t *testing.T) {
	from := day(2026, 1, 1)
	to := from.AddDate(0, 0, 120) // 120-day window, but capped to 90
	buckets := buildDaySeries(from, to, map[string]DayRow{})
	if len(buckets) != 90 {
		t.Fatalf("want 90 buckets (capped), got %d", len(buckets))
	}
	for _, b := range buckets {
		if b.Status != StatusNoData {
			t.Fatalf("empty-history bucket %s = %s, want no_data", b.Start.Format("2006-01-02"), b.Status)
		}
	}
}

// TestBuildHourSeries_48hAndNoData proves a 48h window yields 48 hourly buckets with empty hours as
// no_data.
func TestBuildHourSeries_48hAndNoData(t *testing.T) {
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	from := base
	to := base.Add(48 * time.Hour)
	rows := map[int64]HourAgg{
		base.Unix():                    {Checks: 4, OK: 4, LatSumMS: 200},
		base.Add(2 * time.Hour).Unix(): {Checks: 4, OK: 0, Down: 4, LatSumMS: 400},
	}
	buckets := buildHourSeries(from, to, rows)
	if len(buckets) != 48 {
		t.Fatalf("want 48 hourly buckets, got %d", len(buckets))
	}
	if buckets[0].Status != StatusUp {
		t.Errorf("hour0 = %s want up", buckets[0].Status)
	}
	if buckets[1].Status != StatusNoData {
		t.Errorf("hour1 (no checks) = %s want no_data", buckets[1].Status)
	}
	if buckets[2].Status != StatusDown {
		t.Errorf("hour2 = %s want down", buckets[2].Status)
	}
}

func TestUptimePct(t *testing.T) {
	cases := []struct {
		ok, checks int64
		want       float64
	}{
		{0, 0, 0}, {10, 10, 100}, {1, 4, 25}, {3, 4, 75},
	}
	for _, c := range cases {
		if got := uptimePct(c.ok, c.checks); got != c.want {
			t.Errorf("uptimePct(%d,%d)=%.2f want %.2f", c.ok, c.checks, got, c.want)
		}
	}
}

func TestPercentileInt(t *testing.T) {
	sorted := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if got := percentileInt(sorted, 0.95); got != 100 {
		t.Errorf("p95=%d want 100", got)
	}
	if got := percentileInt(sorted, 0.5); got != 50 {
		t.Errorf("p50=%d want 50", got)
	}
	if got := percentileInt(nil, 0.99); got != 0 {
		t.Errorf("p99(empty)=%d want 0", got)
	}
	if got := percentileInt([]int{7}, 0.99); got != 7 {
		t.Errorf("p99(single)=%d want 7", got)
	}
	// A precise nearest-rank check: q=0.90 over 10 elements => rank ceil(9)=9 => 90.
	if got := percentileInt(sorted, 0.90); got != 90 {
		t.Errorf("p90=%d want 90", got)
	}
}

func TestStatsFrom(t *testing.T) {
	s := statsFrom(WindowSample{Lats: []int{100, 10, 50, 20, 30}, Checks: 5, OK: 4})
	if s.UptimePct != 80 {
		t.Errorf("uptime=%.1f want 80", s.UptimePct)
	}
	if s.AvgLatMS != 42 {
		t.Errorf("avg=%.1f want 42", s.AvgLatMS)
	}
	if s.P95MS != 100 || s.P99MS != 100 {
		t.Errorf("p95=%d p99=%d want 100/100", s.P95MS, s.P99MS)
	}
}

func TestStatusForErr(t *testing.T) {
	if st, ec := statusForErr(nil); st != StatusUp || ec != "" {
		t.Errorf("nil -> %s/%s want up/''", st, ec)
	}
	down := domain.NewProviderError("p", domain.ClassProviderDown, errors.New("x"))
	if st, ec := statusForErr(down); st != StatusDown || ec != "PROVIDER_DOWN" {
		t.Errorf("provider_down -> %s/%s want down/PROVIDER_DOWN", st, ec)
	}
	rl := domain.NewProviderError("p", domain.ClassRateLimit, errors.New("x"))
	if st, _ := statusForErr(rl); st != StatusDegraded {
		t.Errorf("rate_limit -> %s want degraded", st)
	}
	auth := domain.NewProviderError("p", domain.ClassAuth, errors.New("x"))
	if st, _ := statusForErr(auth); st != StatusDown {
		t.Errorf("auth -> %s want down", st)
	}
}

func TestDayBucketStatus_ZeroChecks(t *testing.T) {
	if got := dayBucketStatus(DayRow{Checks: 0}); got != StatusNoData {
		t.Fatalf("zero checks -> %s want no_data", got)
	}
	if got := dayBucketStatus(DayRow{Checks: 5, OK: 5}); got != StatusUp {
		t.Fatalf("all ok -> %s want up", got)
	}
	if got := dayBucketStatus(DayRow{Checks: 5, OK: 3, Down: 2}); got != StatusDegraded {
		t.Fatalf("partial -> %s want degraded", got)
	}
}
