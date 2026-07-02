# 11 — Scaling Strategy

**Status:** `IN-REVIEW` · **Owner:** Staff DevOps Engineer + Cost/Scale Reviewer · **Last updated:** 2026-07-01
**Gated by:** [Cost/Scale Reviewer](../agents/cost-scale-reviewer.md) · `/scale-check`

> Target is a **tested assumption** (`00` §4), validated by load test (`21`), not a measured fact.

## 1. Capacity math (shown)
- 2,000 rec/s × ~3.2 provider calls = **6,400 provider calls/s** (burst 5,000 rec/s → ~16,000 calls/s).
- Little's Law at ~**350 ms** mean provider latency (using p50 as a mean proxy — refine with the
  **measured mean** from `21`): in-flight ≈ 6,400 × 0.35 ≈ **2,240 concurrent calls** steady (consistent
  with `00` §4); size the worker fleet for **4,000–8,000** concurrent to absorb p95 tail + burst.
- Persist ~8 KB/record → ~16 MB/s → ~1.38 TB/day raw (bounded by partitioning + ClickHouse offload, `06`).
> All per-provider latency numbers are `UNVERIFIED` (`03`); the fleet size is recomputed from measured p50/p95.

## 2. Worker & concurrency model
- **Async/event-loop or virtual-thread workers**: each provider call is an await-point, not an OS thread →
  ~40–80 stateless pods, each holding a few hundred in-flight sockets; cheap to add/remove.
- **Per-(provider, key-pool) concurrency budgets**: a distributed token-bucket/semaphore in Redis caps
  aggregate concurrency to each provider's real rate limit (`03`); key-manager rotates across a pool to
  raise the ceiling. **This budget is the primary back-pressure lever.**
  > ⚠️ **`UNVERIFIED` (like latency):** the per-provider rate limits themselves come from `03` and several
  > are UNVERIFIED. **Aggregate achievability of 6,400 calls/s is NOT proven by summing budgets** — it is a
  > load-test question (`21`): the required call mix must fit within the *summed* per-provider budgets (×
  > key-pool size), else the waterfall must widen (more providers/keys) or throttle. Treat the 6,400/s as
  > an assumption gated on that summation being demonstrated.
- **Egress-proxy** scaled with keep-alive/TLS pooling to sustain ~6,400 rps steady / ~16,000 burst.

## 3. Autoscaling (finite — no infinite scale)
| Signal | Scales | Trigger | Cap |
|--------|--------|---------|-----|
| Kafka consumer lag / queue depth | worker pool | lag > threshold | max pods per cell |
| Provider in-flight saturation | (throttle, not scale) | budget exhausted | shed to queue |
| Egress rps / conn-pool utilization | egress-proxy | utilization > 70% | max proxy replicas |
| API p95 / CPU | modulith replicas | p95 SLO breach | max replicas |
Scale-down is gradual with cooldown; every autoscaler has a hard max (cost guardrail).

## 4. Back-pressure & load-shedding
- Bounded queues; when providers saturate, **consumer offsets simply lag** and in-flight stays bounded —
  burst drains without dropping.
- Sync path is capacity-capped and **sheds over-budget single records to async**.
- Priority preserved via partitioning/fair-share (`10`); premium tenants not starved by bulk jobs.

## 5. Failure amplification guard (retries can't breach cost)
Retries are transient-only, capped, and **counted against the G4 cost ceiling** — a provider outage cannot
multiply spend past the ceiling. Breakers cut off failing providers fast; hedging is bounded.

## 6. Multi-region scale
**Regional cells** (ADR-0010) are the scale + blast-radius + residency unit; capacity is sized per cell.
Scale out = "stamp another cell". Cross-region learning shares **non-PII** signals only (`18`).

## 7. Open items
| ID | Item | Status |
|----|------|--------|
| SS-1 | Worker/concurrency sizing | `UNVERIFIED` until load test (needs measured `03` latencies) |
| SS-2 | Autoscaling thresholds + caps | drafted (§3); tune in `21` |
| SS-3 | Load test to validate target | pending (`21`) |

## 8. Reviewer result (`/scale-check` Phase 11)
| Check | Result |
|-------|--------|
| Target restated as assumption + Little's-Law math | PASS |
| Per-provider concurrency budgets bound provider load | PASS |
| Autoscaling has finite caps | PASS |
| Retries can't breach G4 | PASS |
| Back-pressure preserves priority | PASS |
| Numbers depending on uncited latency = `UNVERIFIED` | PASS (honest) |

**Gate:** `GATE-PASS` (auto-advance; recorded — SS-1/SS-3 `ACCEPTED-RISK` pending load test).
