# ADR 0014 — Orchestration: Temporal durable execution (cost-gated), fallback hand-rolled Saga+outbox

- **Status:** Accepted — **conditional on a costed Action-volume spike** (see Verification)
- **Date:** 2026-07-01
- **Deciders:** Distributed Systems Engineer, Cost/Scale Reviewer, Lead Enterprise Solutions Architect
- **Phase:** 10 · **Source:** `docs/10-Queue-System.md` (research `wf_2013b0cd-df8`)

## Context
The async waterfall needs Saga/compensation, timeouts, retries, and checkpoint recovery per record/batch.
Every broker (ADR-0013) is a *transport, not an orchestrator* — with any of them we still hand-write the
saga state machine, compensating actions, and (for DB-atomic steps) a transactional outbox + relay. That
is the highest-risk code a small team could own.

## Options considered
| Option | Pros | Cons | Tradeoff |
|--------|------|------|----------|
| **Temporal durable execution (chosen, gated)** | deletes saga/outbox/checkpoint code (event-sourced History = checkpoint recovery); **native weighted tenant fairness** (fixes ADR-0013 whale HOL-blocking); native at-least-once + retries | **Action-metered cost** at scale; heavy self-host ops; a big stateful service (**tension with ADR-0010's ops-simplicity**) | maintenance-risk vs per-Action cost + a new heavy dependency |
| Hand-rolled Saga + transactional outbox + checkpoint on the broker | no new engine; full portability | large correctness-sensitive codebase to build + maintain; no built-in fairness | control vs burden |

## Decision
**Adopt Temporal** to orchestrate the per-record/per-batch waterfall (Saga in workflow code, RetryPolicy
per Activity, event-sourced checkpoint recovery, Task-Queue **fairness** for weighted per-tenant scheduling)
— **conditional on a costed Action-volume spike passing**. Architectural shape that resolves the cost
tension: keep **Redpanda/Kafka as the high-volume ingestion + lag-backpressure buffer** (ADR-0013);
Temporal orchestrates the waterfall, **not** raw transport (do **not** push 6,400 msg/s through Temporal as
a queue). Minimize Actions (batch records per workflow; sparse heartbeats). **Redis KV** remains the
idempotency store. A small transactional **outbox stays only at boundaries** where a separate service must
atomically flip its own DB row and emit an event outside Temporal.

**Documented fallback (if the spike fails):** hand-rolled **Saga + transactional outbox + checkpoint** on
the Redpanda backbone (Debezium/CDC relay), accepting the higher build/maintenance cost.

## Rationale
For a small team the ongoing correctness+maintenance burden of bespoke distributed-transaction code
outweighs per-Action cost, and Temporal *also* fixes ADR-0013's tenant-fairness gap and deletes the
checkpoint store. We explicitly surface the tension with ADR-0010 (a new heavy stateful service vs
ops-simplicity) and manage it by (a) cost-gating, (b) keeping Temporal off the raw-transport path, and
(c) preferring Temporal Cloud/managed to avoid self-host ops.

## Consequences
- Positive: deletes the highest-risk code; native fairness + checkpoint recovery.
- Negative/accepted: Action cost + a new dependency; requires the spike before commit → **open item QS-TMP-1**.

## Verification
**Costed spike (blocking this ADR's unconditional acceptance):** model realistic Actions-per-record ×
volume against Temporal Cloud pricing + provisioned capacity; measure self-host ops if considered. If
untenable → fall back to hand-rolled Saga+outbox. Flagged for the human at the Phase-16 consolidated review.
