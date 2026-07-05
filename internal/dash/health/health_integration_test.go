//go:build integration

// Live-Postgres proof for the Provider Health Center (P4 acceptance #4) over migration 0009 under
// FORCE RLS as a NON-superuser role (superusers bypass RLS, proving nothing):
//
//   - raw provider_health_checks seeded across days fold into provider_health_1d with correct
//     counts/uptime and a worst_error_class;
//   - Timeline(day) returns a contiguous 90-day series from the fold, and Timeline(hour) a 48-hour
//     series from raw checks;
//   - ACCEPTANCE #4: a Provider (or a day/hour) with ZERO checks renders no_data, NEVER up.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package health_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/health"
	"github.com/enrichment/waterfall/internal/pg"
)

const healthRole = "dash_health"

// Tables migration 0009 creates (dropped/rebuilt for a clean run). Health needs only the last two,
// but 0009 is applied whole, so the whole set is torn down.
var telemetryTables = []string{
	"usage_events", "provider_stats_1m", "provider_stats_1h", "provider_stats_1d",
	"key_usage_1m", "key_usage_1h", "key_usage_1d", "tenant_usage_1h", "tenant_usage_1d",
	"cost_rollup_1d", "queue_stats_1m", "queue_stats_1h", "worker_heartbeats", "worker_stats_5m",
	"provider_health_checks", "provider_health_1d",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the health integration test")
	}
	return pg.ParseDSN(d)
}

func tryExec(c *pg.Conn, sql string) { _ = c.Exec(sql) }

func mustExec(t *testing.T, c *pg.Conn, sql string, args ...any) {
	t.Helper()
	if err := c.ExecParams(sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func scalar(t *testing.T, c *pg.Conn, sql string) string {
	t.Helper()
	res, err := c.Query(sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return ""
	}
	return *res.Rows[0][0]
}

func setupHealthSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+healthRole+" cascade")
	tryExec(admin, "drop role if exists "+healthRole)
	tryExec(admin, "drop table if exists "+strings.Join(telemetryTables, ", ")+" cascade")

	// The dual-GUC accessor functions the 0009 policies reference (0001/0004 in production).
	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)
	mustExec(t, admin, `create or replace function app_current_role() returns text
		language sql stable as $$ select current_setting('app.current_role', true) $$`)

	ddl, err := os.ReadFile("../../../migrations/0009_dash_telemetry.sql")
	if err != nil {
		t.Fatalf("read migration 0009: %v", err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply migration 0009: %v", err)
	}

	mustExec(t, admin, "create role "+healthRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on provider_health_checks, provider_health_1d to "+healthRole)
}

func TestHealthTimelineFoldAndNoData(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupHealthSchema(t, admin)

	appCfg := cfg
	appCfg.User = healthRole
	pool := pg.NewPool(appCfg, 8)
	defer pool.Close()
	store := db.New(pool)
	pgs := health.NewPGStore(store)
	svc := health.NewService(health.Deps{Store: store})
	ctx := context.Background()

	ref := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	dayOf := func(delta int) time.Time {
		d := ref.AddDate(0, 0, delta)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	}

	// --- seed raw checks across days for "hunter" (D-3 all up; D-2 mostly down; D-1 empty) ---
	seed := func(provider string, at time.Time, res health.CheckResult) {
		if err := pgs.WriteCheck(ctx, provider, res, at); err != nil {
			t.Fatalf("seed %s @ %s: %v", provider, at.Format(time.RFC3339), err)
		}
	}
	up := health.CheckResult{Status: "up", LatencyMS: 50}
	down := health.CheckResult{Status: "down", LatencyMS: 200, ErrorClass: "PROVIDER_DOWN"}

	d3 := dayOf(-3).Add(12 * time.Hour)
	for i := 0; i < 4; i++ {
		seed("hunter", d3.Add(time.Duration(i)*time.Minute), up)
	}
	d2 := dayOf(-2).Add(12 * time.Hour)
	seed("hunter", d2, up)
	for i := 0; i < 3; i++ {
		seed("hunter", d2.Add(time.Duration(i+1)*time.Minute), down)
	}
	// "empty" provider: NO checks anywhere.

	// --- seed raw checks in the last 48h for the hour view ---
	h3 := ref.Add(-3 * time.Hour)
	for i := 0; i < 4; i++ {
		seed("hunter", h3.Add(time.Duration(i)*time.Minute), up)
	}
	h1 := ref.Add(-1 * time.Hour)
	seed("hunter", h1, down)
	seed("hunter", h1.Add(time.Minute), up)
	seed("hunter", h1.Add(2*time.Minute), up)

	// --- fold the seeded days ---
	for _, d := range []int{-3, -2, -1} {
		if _, err := svc.FoldDay(ctx, dayOf(d)); err != nil {
			t.Fatalf("fold day %d: %v", d, err)
		}
	}

	// Fold math on provider_health_1d (verified as superuser; RLS-bypass is fine for an assertion).
	d2Str := dayOf(-2).Format("2006-01-02")
	if got := scalar(t, admin, "select checks||'/'||ok||'/'||down||'/'||coalesce(worst_error_class,'') from provider_health_1d where provider_id='hunter' and day='"+d2Str+"'"); got != "4/1/3/PROVIDER_DOWN" {
		t.Fatalf("D-2 fold = %q, want 4/1/3/PROVIDER_DOWN", got)
	}

	// --- Timeline (day): 90 contiguous buckets from the fold ---
	from := dayOf(-89)
	to := ref
	tl, err := svc.Timeline(ctx, "hunter", from, to, "day")
	if err != nil {
		t.Fatalf("timeline day: %v", err)
	}
	if len(tl.Buckets) != 90 {
		t.Fatalf("day timeline buckets=%d, want 90", len(tl.Buckets))
	}
	byDay := map[string]string{}
	for _, b := range tl.Buckets {
		byDay[b.Start.Format("2006-01-02")] = b.Status
	}
	if s := byDay[dayOf(-3).Format("2006-01-02")]; s != "up" {
		t.Errorf("D-3 (all up) bucket = %s, want up", s)
	}
	if s := byDay[dayOf(-2).Format("2006-01-02")]; s != "down" {
		t.Errorf("D-2 (mostly down) bucket = %s, want down", s)
	}
	// ACCEPTANCE #4: the empty day is no_data, never up.
	if s := byDay[dayOf(-1).Format("2006-01-02")]; s != "no_data" {
		t.Fatalf("D-1 (zero checks) bucket = %s, want no_data (acceptance #4)", s)
	}

	// --- Timeline (day) for the zero-check provider: EVERY bucket no_data, NONE up ---
	empty, err := svc.Timeline(ctx, "empty", from, to, "day")
	if err != nil {
		t.Fatalf("timeline empty: %v", err)
	}
	if len(empty.Buckets) != 90 {
		t.Fatalf("empty-provider day buckets=%d, want 90", len(empty.Buckets))
	}
	for _, b := range empty.Buckets {
		if b.Status != "no_data" {
			t.Fatalf("zero-check provider bucket %s = %s, want no_data (never up)", b.Start.Format("2006-01-02"), b.Status)
		}
	}

	// --- Timeline (hour): 48 contiguous buckets from raw checks ---
	htl, err := svc.Timeline(ctx, "hunter", ref.Add(-48*time.Hour), ref, "hour")
	if err != nil {
		t.Fatalf("timeline hour: %v", err)
	}
	if len(htl.Buckets) != 48 {
		t.Fatalf("hour timeline buckets=%d, want 48", len(htl.Buckets))
	}
	byHour := map[int64]string{}
	nonEmpty := 0
	for _, b := range htl.Buckets {
		byHour[b.Start.Unix()] = b.Status
		if b.Status != "no_data" {
			nonEmpty++
		}
	}
	truncHour := func(x time.Time) time.Time {
		x = x.UTC()
		return time.Date(x.Year(), x.Month(), x.Day(), x.Hour(), 0, 0, 0, time.UTC)
	}
	if s := byHour[truncHour(h3).Unix()]; s != "up" {
		t.Errorf("h-3 hour bucket = %s, want up", s)
	}
	if s := byHour[truncHour(h1).Unix()]; s != "degraded" {
		t.Errorf("h-1 hour bucket (2 up / 1 down) = %s, want degraded", s)
	}
	if nonEmpty != 2 {
		t.Errorf("non-empty hour buckets=%d, want 2 (rest no_data)", nonEmpty)
	}

	t.Logf("PASS acceptance #4: 90 day-buckets + 48 hour-buckets; fold D-2=4/1/3/PROVIDER_DOWN; zero-check day and zero-check provider both render no_data (never up)")
}
