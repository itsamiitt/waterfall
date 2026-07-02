# 09 — Execution Engine

**Status:** `IN-REVIEW` · **Owner:** Distributed Systems Engineer · **Last updated:** 2026-07-01
**Gated by:** [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) · [api-integration](../skills/api-integration/SKILL.md) · `/architecture-review`

> The deterministic **spine**: it executes an `ExecutionPlan` (`08`) against provider-adapters with all
> five gates + SSRF, and it is the component that **re-enforces G3/G4 before every call** regardless of
> what the router proposed. Confidence = ADR-0005; merge = ADR-0006; identity = ADR-0004.

## 1. Plan execution
- Runs plan steps in the specified **parallel prefix** then **sequential** tail; **early-stops** on SPRT
  confidence target / G4 ceiling / G3 timeout.
- Deployed as the stateless **worker pool** (`05`); sync path runs the same library inline (`04` §3).

## 2. Per-call discipline (structural)
| Gate | Enforcement (before/around every provider call) |
|------|--------------------------------------------------|
| **G2 idempotency** | key = `hash(tenant, record, field, provider, params, config_version)`; check `idempotency_ledger` (Postgres unique) + Redis fast-path; replay returns stored result, **no second paid call**; Thompson RNG seeded from the key |
| **G3 bounded** | per-call connect+total timeout, max attempts, jittered backoff, per-(provider,key,region) **circuit breaker** (state in Redis, shared across workers); deadline propagated from the plan; hedge/backup-provider on timeout |
| **G4 cost ceiling** | re-validate the credit **reservation** (Redis-atomic, Postgres-backed) before each call; failed calls **compensate/refund**; never call without a confirmed reservation |
| **SSRF** | every socket exits via the **egress-proxy** (`13`); workers have no internet route |

## 3. Result handling (G5)
- **Normalize** provider output to canonical Field names (`00` §7); **defensive typing** (some providers
  return booleans instead of values on metered plans — `01` K10) — type-check every field.
- **Calibrate** (per provider,field) → **log-odds fuse** (weight caps + correlation discount) → **SPRT**
  (ADR-0005).
- **Deterministic merge** (ADR-0006): reliability-weighted resolution + freshness decay + tenant authority
  + explicit tie-break; **keep all candidates (winners + losers)**.
- **Provenance write:** merge-then-write into `field_versions` with a `NOT NULL` provenance FK — a bare
  value cannot be persisted; config-versions pinned for deterministic re-resolution.

## 4. Fallback & error taxonomy
Errors map to the `api-integration` canonical taxonomy; router actions: `NOT_FOUND` → next step (not an
error); `TRANSIENT` → capped retry then next step; `RATE_LIMIT`(incl. Hunter 403) → backoff/rotate key;
`QUOTA`(incl. 402) → disable key + failover; `AUTH` → disable key + alert; `PROVIDER_DOWN`(breaker) →
skip. Every fallback records a reason (provenance/audit).

## 5. Checkpointing & recovery (async)
Per `job_item` progress is checkpointed so retries/redeliveries **resume, not restart** (idempotent,
G2). In the async lane this is provided by **Temporal's event-sourced workflow History** (Saga +
checkpoint recovery, [ADR-0014](../adr/0014-orchestration-temporal-cost-gated.md) — cost-gated; fallback =
hand-rolled saga + outbox on the Kafka offset log). Poisoned/exhausted items → per-stage **DLQ** (`10`).
Retry semantics: [`retry-flow.mmd`](../diagrams/retry-flow.mmd).

## 6. Webhook fan-in
Async providers return `202` + deliver via HMAC-signed webhook → an **idempotent, signature-verified**
receiver; inbound results dedupe on the idempotency key; outbound webhooks to tenants leave via the
egress-proxy.

## 7. Interfaces
- In: `ExecutionPlan` + `ctx{tenant_id, deadline, idempotency_seed}`.
- Out: `EnrichedRecord{fields[]{value, confidence, provider, cost, fetched_at, prov_lineage}}` + cost ledger.

## 8. Open items
| ID | Item | Status |
|----|------|--------|
| EE-1 | Checkpoint/resume model | drafted; detail with `10` |
| EE-2 | Cost reservation/reconciliation API | ✅ (cost/billing, `16`) |
| EE-3 | Hedge/backup-provider policy on timeout | drafted; tune with `11`/`21` |

## 9. Reviewer result (`/gate-check` Phase 9)
| Check | Result |
|-------|--------|
| G2/G3/G4 re-enforced before every call (structural) | PASS |
| G5 provenance: no bare write; losers retained; config-versioned | PASS |
| SSRF: all egress via proxy | PASS |
| Defensive typing + normalization | PASS |
| Fallback taxonomy incl. provider quirks (402/Hunter-403) | PASS |
| Checkpoint/resume idempotent | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
