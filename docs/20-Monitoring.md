# 20 — Monitoring & Observability

**Status:** `IN-REVIEW` · **Owner:** Staff DevOps Engineer · **Last updated:** 2026-07-01
**Gated by:** [Architecture Reviewer](../agents/architecture-reviewer.md) · `/architecture-review`

## 1. Stack
Prometheus (metrics) · Grafana (dashboards, feeds `17`) · OpenTelemetry (distributed traces across
edge→modulith→workers→egress) · ELK/equivalent (logs). Managed equivalents per cloud (ADR-0015).

## 2. Golden signals (per service + per provider + per tenant)
latency (p50/p95/p99) · traffic · errors (by canonical taxonomy) · saturation (in-flight, queue lag, pool util).

## 3. Enrichment-specific metrics
provider success-rate · coverage by field · **cost/record** · **cache hit-rate** · queue depth/consumer
lag · circuit-breaker state · retry/DLQ counts · confidence distribution · SPRT stop-reason mix.

## 4. Waterfall KPIs (`01` K11 — prove the value prop)
per-provider **hit-rate**, **incremental lift** (coverage added vs prior tier — Clay reports ~3× vs
single-source), **cost-per-match**; these tune ordering/reservation values (`08`).

## 5. Provider supply-continuity (`01` K8)
Monitor API sunsets/takedowns + sustained per-provider error spikes → alert + feed `12` auto-failover.

## 6. SLOs, alerts, security telemetry
- **SLOs + error budgets** per public API (`07`); alert → **runbook** links; on-call routing.
- **Security telemetry:** cross-tenant access attempts, SSRF blocks, auth failures, secret-access anomalies
  → security alerts + audit (`18`).
- Progressive-rollout guard: canary SLO breach → automated rollback (`19`).

## 7. Cardinality & privacy
tenant_id/provider labels bounded; **no PII in metrics/logs** (G1); logs scrubbed of secrets/PII.

## 8. Open items
| ID | Item | Status |
|----|------|--------|
| MO-1 | SLOs + error budgets per API | drafted; finalize with `21` |
| MO-2 | Metric catalog + cardinality plan | drafted §3–§4 |
| MO-3 | Alert→runbook mapping | at impl |

## 9. Reviewer result
| Check | Result |
|-------|--------|
| Golden signals + enrichment KPIs + waterfall lift | PASS |
| SLOs/error budgets + alert→runbook | PASS |
| Security telemetry (SSRF/cross-tenant) | PASS |
| No PII in telemetry; bounded cardinality | PASS |
| Continuity monitoring → failover | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
