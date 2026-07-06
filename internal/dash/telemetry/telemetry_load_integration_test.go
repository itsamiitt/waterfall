//go:build integration

// Load proof for the rollup fold at 1M synthetic usage_events (doc 13 §6 L4; closes the L4
// single-instance measurement for OI-TS-3). It folds one million events over a bucket-aligned
// window and records the MEASURED single-instance timing written back into doc 13 §6 L4. This is a
// dev single-instance measurement, NOT the staging target; the multi-instance / retention-window
// run stays deferred to the staging load-lab (OI-P12-1).
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/telemetry"
	"github.com/enrichment/waterfall/internal/pg"
)

// seedUsageEventsN inserts n deterministic usage_events server-side (as the superuser admin, so RLS
// is bypassed for seeding) with the same spread as the 100k acceptance seed: 3 Tenants, 5 Providers,
// 10 Keys, 3 workflows, 3 countries, 180 one-minute buckets on 2026-07-01. Rollup cardinality is
// therefore identical to the 100k test; only the raw-event volume scales.
func seedUsageEventsN(t *testing.T, admin *pg.Conn, n int) {
	t.Helper()
	mustExec(t, admin, `insert into usage_events
	  (tenant_id, provider_id, key_id, workflow_key, country, outcome_class, credits, lat_ms, created_at)
	  select
	    (array['acme','globex','initech'])[1 + (g % 3)],
	    (array['hunter','clearbit','apollo','zoominfo','lusha'])[1 + (g % 5)],
	    ('00000000-0000-4000-8000-' || lpad((g % 10)::text, 12, '0'))::uuid,
	    (array['enrich_email','enrich_phone','enrich_company'])[1 + (g % 3)],
	    (array['us','eu','apac'])[1 + (g % 3)],
	    (array['ok','ok','ok','ok','ok','ok','AUTH','RATE_LIMIT','TRANSIENT','NOT_FOUND','BAD_REQUEST','QUOTA','PROVIDER_DOWN','UNKNOWN'])[1 + (g % 14)],
	    (g % 5),
	    (g * 7) % 9000,
	    timestamptz '2026-07-01 00:00:00+00' + ((g % 180) * interval '1 minute')
	  from generate_series(1, `+itoa(n)+`) g`)
}

// TestFold1M folds one million usage_events and records the measured single-instance timing. It is
// an ON-DEMAND load fixture (docs/13 §1 "Load: on demand", §6 L4): seeding + folding 1,000,000
// events is heavy for the routine RLS gate, so it SKIPS under -short (which scripts/run-rls-test.sh
// passes). The P12 measurement in docs/13 §6 was captured by running it on-demand WITHOUT -short; the
// lighter 100k proof (TestTelemetryFoldRefoldIdentical) stays in the default gate.
func TestFold1M(t *testing.T) {
	if testing.Short() {
		t.Skip("on-demand load fixture (1M events); runs without -short (docs/13 §6 L4)")
	}
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupTelemetrySchema(t, admin)

	const n = 1000000
	seedStart := time.Now()
	seedUsageEventsN(t, admin, n)
	if got := scalar(t, admin, "select count(*) from usage_events"); got != itoa(n) {
		t.Fatalf("seeded usage_events = %q, want %d", got, n)
	}
	seedDur := time.Since(seedStart)

	store, closeStore := appStore(t, cfg)
	defer closeStore()

	fixedNow := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	agg := telemetry.NewAggregator(store, func() time.Time { return fixedNow }, nil)
	ctx := context.Background()
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 1, 4, 0, 0, 0, time.UTC) // bucket-aligned; covers the 180-minute span

	foldStart := time.Now()
	folded, err := agg.Refold(ctx, from, to)
	if err != nil {
		t.Fatalf("refold 1M: %v", err)
	}
	foldDur := time.Since(foldStart)

	if folded != n {
		t.Fatalf("refold folded %d events, want %d", folded, n)
	}
	if ps := scalar(t, admin, "select count(*) from provider_stats_1m"); ps == "0" || ps == "" {
		t.Fatalf("provider_stats_1m empty after 1M fold")
	}
	// Sanity: the fold accounted for every raw event (sum(req) over the finest provider grain).
	if got := scalar(t, admin, "select coalesce(sum(req),0) from provider_stats_1m"); got != itoa(n) {
		t.Fatalf("provider_stats_1m sum(req) = %q, want %d (fold must count every event once)", got, n)
	}

	rate := float64(n) / foldDur.Seconds()
	t.Logf("MEASURED 1M fold (dev, single instance): seeded %d usage_events in %s; Refold [%s,%s) folded %d events in %s (%.0f events/s); provider_stats_1m sum(req)=%d",
		n, seedDur.Round(time.Millisecond), from.Format("15:04"), to.Format("15:04"), folded, foldDur.Round(time.Millisecond), rate, n)
}
