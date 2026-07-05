// Package telemetry is the dashboard's telemetry backbone (doc 12 §P4, doc 03 §2.6, doc 10
// §2): the single hot-path writer of the raw usage_events feed and the leader-elected
// aggregator that folds every rollup family from it. It also hosts the runtime partition
// maintainer (doc 03 §4), the telemetry-backed key-budget reconcile, and the bounded rollup
// read helpers the cost/overview phases consume.
//
// Data flow (doc 10 §2): the engine hot path performs exactly ONE INSERT per Provider call
// into usage_events (Class T — tenant-isolation RLS, doc 03 §1). Everything the dashboard
// serves is folded from that stream by the leader aggregator (advisory lock 'dash_aggregator')
// with additive upserts; a repair refold recomputes whole buckets and replaces. Latency is
// stored as a fixed 20-bucket log-spaced histogram (this file); percentiles are computed at
// read, never stored.
//
// Gates: G1 tenant isolation. usage_events is Class T, so both the Recorder (write) and the
// aggregator (read) bind app.current_tenant per transaction — the Recorder to the event's
// Tenant, the aggregator by iterating the operator-readable Tenant registry and folding each
// Tenant under its own dual-GUC transaction (doc 03 §9.4, OI-DB-3). Class P rollups
// (provider_stats_*, key_usage_*) are accumulated in memory across the pass and written under
// app.current_tenant='platform'. No PII/secrets are ever logged.
package telemetry

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// UsageEvent is one Provider call's telemetry — the exact row the engine hot path appends to
// usage_events (doc 10 §2). Dimensions are bounded, low-cardinality attribution values; no PII.
type UsageEvent struct {
	TenantID     string // owning Tenant (RLS key); required
	ProviderID   string // Provider that served the call; required
	KeyID        string // Provider Key id (uuid text), or "" when no key was used
	WorkflowKey  string // workflow attribution, or ""
	Country      string // subject country, or ""
	OutcomeClass string // "ok" or one of the 8 error classes (see OutcomeOK / domain.ErrorClass)
	Credits      int64  // credits spent on this call
	LatMs        int    // observed latency in milliseconds
}

// OutcomeOK is the success sentinel for OutcomeClass; every other value is one of the 8-class
// error taxonomy (domain.ErrorClass.String(): AUTH, RATE_LIMIT, TRANSIENT, NOT_FOUND,
// BAD_REQUEST, QUOTA, PROVIDER_DOWN, UNKNOWN).
const OutcomeOK = "ok"

// Resolution is a rollup time granularity. The 1m/1h/1d families exist for provider_stats and
// key_usage; tenant_usage has 1h/1d; cost_rollup is 1d only (doc 03 §2.6).
type Resolution string

const (
	Res1m Resolution = "1m"
	Res1h Resolution = "1h"
	Res1d Resolution = "1d"
)

// Sink is the fire-and-forget seam the rotation Done path calls on every Provider call
// (integration wires the concrete Sink; this package provides the types). Record never blocks
// on the database and never returns an error to the hot path — overflow is dropped with a
// metric (BufferedRecorder) — so a telemetry stall can never wedge enrichment.
type Sink interface {
	Record(ctx context.Context, ev UsageEvent)
}

// failCols are the 8 provider_stats failure columns, ordered to match the error taxonomy
// (doc 03 §2.6). failIndex maps an OutcomeClass string onto this order; an unrecognized class
// folds into fail_unknown (fail-safe: telemetry never drops a row for an unknown class).
var failCols = [8]string{
	"fail_auth", "fail_rate_limit", "fail_transient", "fail_not_found",
	"fail_bad_request", "fail_quota", "fail_provider_down", "fail_unknown",
}

func failIndex(outcome string) int {
	switch outcome {
	case "AUTH":
		return 0
	case "RATE_LIMIT":
		return 1
	case "TRANSIENT":
		return 2
	case "NOT_FOUND":
		return 3
	case "BAD_REQUEST":
		return 4
	case "QUOTA":
		return 5
	case "PROVIDER_DOWN":
		return 6
	default: // UNKNOWN and any unrecognized class
		return 7
	}
}

// latBucketsMs are the upper bounds (inclusive) of the 20 fixed log-spaced latency histogram
// buckets, in milliseconds — roughly a doubling scale from 1ms to ~256s, with a catch-all top
// bucket. Postgres does not enforce array bounds, so lat_hist is always written as exactly
// len(latBucketsMs) elements and this table is the single source of truth (doc 03 §2.6, doc 10
// §2). The boundaries are code-enforced and MUST stay stable: changing them re-buckets history.
var latBucketsMs = [20]int64{
	1, 2, 4, 8, 16, 32, 64, 125, 250, 500,
	1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 1<<62 - 1,
}

// histBuckets is the fixed histogram width (20).
const histBuckets = len(latBucketsMs)

// bucketIndex returns the 0..19 histogram bucket for a latency in milliseconds. A latency at or
// below latBucketsMs[i] (and above the previous bound) lands in bucket i; the top bucket is a
// catch-all so every finite latency is counted exactly once.
func bucketIndex(latMs int64) int {
	for i, ub := range latBucketsMs {
		if latMs <= ub {
			return i
		}
	}
	return histBuckets - 1
}

// histLiteral renders a 20-element histogram as a Postgres bigint[] text literal '{n1,...,n20}'
// for a bound parameter (the internal/pg client encodes params as text and has no []int64
// encoder). The array is always full width.
func histLiteral(h *[histBuckets]int64) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range h {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(v, 10))
	}
	b.WriteByte('}')
	return b.String()
}

// bucketStart truncates t to the start of its 1-minute, 1-hour, or 1-day bucket in UTC. Day
// truncation is exact against the UTC epoch (Go time carries no leap seconds), so day buckets
// align to UTC midnight regardless of local zone.
func bucketStart(t time.Time, res Resolution) time.Time {
	t = t.UTC()
	switch res {
	case Res1m:
		return t.Truncate(time.Minute)
	case Res1h:
		return t.Truncate(time.Hour)
	case Res1d:
		return t.Truncate(24 * time.Hour)
	default:
		return t.Truncate(time.Minute)
	}
}
