# 29 — Implementation Slice 07: observability (metrics + structured logs) (Go)

**Status:** `IMPLEMENTED` (tests green + live /metrics smoke) · **Owner:** Staff DevOps Engineer · **Last updated:** 2026-07-01
**Builds on:** [`28`](28-Implementation-Slice-06.md) · **Canonical spec:** [`20`](20-Monitoring.md) · **Approved by:** human (2026-07-01)

> Makes the whole stack observable: a dependency-free Prometheus registry, RED golden
> signals at the API, enrichment KPIs at the engine, and structured request logs — with
> cardinality/PII discipline.

## 1. Metrics registry (`internal/metrics`)
A ~230-line, concurrency-safe registry rendering the **Prometheus text exposition format**
— Counters, Gauges, GaugeFuncs, Histograms with labels — no client-library dependency.
Cardinality rule enforced by convention (documented in the package): **bounded label values
only; never PII or unbounded ids**.

## 2. What is instrumented
| Signal | Metric | Where |
|--------|--------|-------|
| **RED / golden signals** | `http_requests_total{route,method,status}`, `http_request_duration_seconds{route}` | API middleware (`instrument`) |
| Provider health | `provider_calls_total{provider,result}` (result ∈ success/no_value/error-class/**breaker_open**/**blocked**), `provider_call_duration_seconds{provider}` | Engine |
| **Enrichment KPIs** (docs/20 §4) | `provider_cost_credits_total{provider}`, `enrichment_fields_filled_total{field,provider}` | Engine |
| Saturation | `queue_depth` (GaugeFunc) | cmd wiring |
| Delivery | `webhook_deliveries_total{result}` | cmd OnComplete |

`GET /metrics` serves them. **SSRF blocks and breaker trips surface as provider-call result
labels** (`blocked`, `breaker_open`) — no extra wiring, and they reuse the Slice-05/03 errors.

## 3. Structured logs + PII discipline
The API `instrument` middleware emits one structured `slog` line per request
(`method, route, status, dur_ms`) using the **route template**, never the concrete path — so
a subject id or email never enters a log line or a metric label. `TestMetricsEndpoint` asserts
a concrete id (`some-secret-id`) does **not** appear in `/metrics`.

## 4. Tests (7 new; 72 total)
`metrics`: counter/gauge/gaugefunc/histogram rendering, label escaping, re-register safety.
`api`: `/metrics` exposes RED with the `{id}` template label and **no leaked id**. `engine`:
provider success + fields-filled + cost metrics emitted. **Live smoke:** submitted a job, scraped
`/metrics`, saw per-vendor calls, `provider_cost_credits_total` summing to 13 (the waterfall
spend), `enrichment_fields_filled_total`, `queue_depth`, and `http_requests_total`.

## 5. Honestly out of this slice
- **Tracing** (OpenTelemetry spans across edge→engine→egress, docs/20) — not yet; only
  metrics + logs.
- **Exemplars, native histograms, /metrics auth**, and a scrape-config/Grafana dashboards.
- Per-tenant metrics are intentionally **omitted** to bound cardinality; tenant-level usage
  belongs in the billing/ClickHouse path (docs/06), not Prometheus labels.
- Breaker-state and idempotency-hit gauges (currently inferred via result labels).

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Golden signals (RED) exposed at the API | PASS |
| Enrichment KPIs (cost, fields, provider result) exposed | PASS |
| SSRF blocks + breaker trips visible (result labels) | PASS |
| No PII / unbounded ids in labels or logs (tested) | PASS |
| Prometheus text format valid; `/metrics` served | PASS |
| `go build/vet/test/gofmt` clean | PASS |
| Tracing + dashboards honestly deferred (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; proceeds to the next increment on request.
