# 10 — Queue System

**Status:** `IN-REVIEW` · **Owner:** Distributed Systems Engineer + Staff DevOps Engineer · **Last updated:** 2026-07-01
**Gated by:** [Cost/Scale Reviewer](../agents/cost-scale-reviewer.md) · `/scale-check` + `/architecture-review`

> **Two orthogonal decisions:** transport **[ADR-0013](../adr/0013-async-transport-kafka-log.md)** (Kafka-
> protocol log, Redpanda preferred) and orchestration **[ADR-0014](../adr/0014-orchestration-temporal-cost-gated.md)**
> (Temporal, cost-gated). Research `wf_2013b0cd-df8` (7 technologies, cited). Diagrams:
> [`queue-flow.mmd`](../diagrams/queue-flow.mmd), [`retry-flow.mmd`](../diagrams/retry-flow.mmd), [`event-flow.mmd`](../diagrams/event-flow.mmd).

## 1. Requirements (what eliminates options)
partition-by-tenant fairness · at-least-once + app idempotency · **back-pressure via consumer lag** ·
per-stage DLQ · retry queues · checkpoint recovery · **multi-cloud portability** · ~6,400 provider-calls/s.
Two are decisive: **lag-based back-pressure** and **multi-cloud portability**.

## 2. Technology tradeoff matrix (cited in ADR-0013)
| Req | Kafka/Redpanda | RabbitMQ QQ | Redis Streams | SQS(+SNS) | Pub/Sub | NATS JetStream | Temporal |
|-----|----------------|-------------|---------------|-----------|---------|----------------|----------|
| Partition-by-tenant | **native (key hash)** | hand-rolled | hand-rolled | MsgGroupId | ordering key | client-lib | **fairness keys (best)** |
| At-least-once + idempotency | native+app | native+app | native+app | native+app | native+app | native+app | **native** |
| DLQ | build | **native DLX** | build | **native redrive** | **native** | build | park workflow |
| Back-pressure **via lag** | **native** | credit/confirm | observe only | depth proxy | flow-control | **pull lag** | backlog |
| Multi-cloud portable | **yes (protocol)** | yes | yes | **AWS-only** | **GCP-only** | Synadia-managed | SaaS/self-host |
| Ops (small team) | med (Redpanda↓) | med-high | med | **lowest** | **lowest** | low | high self-host / $ |
| ~6,400 calls/s | trivial | many-queue only | single-core-bound | trivial | trivial | trivial | costly (Actions) |

**Chosen:** Kafka-protocol log (Redpanda) for transport; Temporal for orchestration (gated); Redis KV as
the idempotency store. **Losers:** SQS/Pub/Sub (single-cloud), RabbitMQ (wrong back-pressure model, no
replay), Redis Streams (durability — secondary only), NATS (managed-multicloud maturity, runner-up).

## 3. Placement (ADR-0010/0013/0014)
Kafka sits **only** on the async boundary (enrichment-api → worker pool) + outbound events; the sync path
never touches it. **Temporal orchestrates the waterfall** on top of the workers (not raw transport). Intake
topic **partition = tenant_id** (fairness/ordering), **hash = record-id** (idempotent placement). Consumer
**lag = back-pressure + autoscale signal** (`11`).

## 4. Required patterns — how each is realized
| Pattern | Realization |
|---------|-------------|
| **Idempotency** | key `hash(tenant,record,field,provider,params,config_ver)`; Redis KV + Postgres ledger (G2) |
| **At-least-once** | Kafka default; end-to-end effective-once is **app-level** (Kafka EOS stops at Kafka; providers are HTTP) |
| **Circuit breaker** | per-(provider,key,region), Redis-shared state (G3) |
| **Retry queues** | tiered retry topics / Temporal RetryPolicy; jittered backoff, capped, **cost-counted (G4)** — [`retry-flow.mmd`](../diagrams/retry-flow.mmd) |
| **DLQ** | per-stage DLQ topics (built; Kafka has no broker-native DLQ) + ops replay (idempotent) |
| **Saga** | Temporal workflow code (compensation in reverse); fallback hand-rolled saga (ADR-0014) |
| **Outbox** | With Temporal, its History covers orchestrated effects. **The concrete non-Temporal write→publish boundaries that DO need a transactional outbox** (and the ones that carry the whole design in the hand-rolled-saga fallback, ADR-0014): (1) `enrichment-api` job/job_items **persist → Kafka enqueue**; (2) **result persist (`field_versions`) → outbound results/webhook topic**; (3) **cost_ledger commit → billing/usage event**. Mechanism: write the row + an `outbox` row in one Postgres txn; a **CDC relay (Debezium)** tails the outbox → publishes to Kafka exactly-once-effective; consumers dedupe on the event id (G2). This path is fully specified so the fallback is buildable, not sketched. |
| **Checkpoint recovery** | Kafka offset commit + replay; Temporal event-sourced History resumes a half-run waterfall |
| **Distributed locks** | Redis (fencing tokens) for the few mutually-exclusive ops (e.g. key rotation) |
| **Worker pools** | stateless execution-engine workers; scale on lag (`11`) |
| **Priority queues** | separate priority topics / Temporal priority tiers (Kafka has no native priority) |
| **Autoscaling workers** | lag/depth-driven, finite caps (`11`) |
| **Distributed scheduling** | Temporal schedules / cron for offline-learning + retries; fair per-tenant scheduling |
| **CQRS** | writes → Postgres OLTP; reads/analytics → ClickHouse via CDC (`06`) |

## 5. Open items
| ID | Item | Status |
|----|------|--------|
| QS-1 | Queue tradeoff + engine | ✅ ADR-0013 (Redpanda) |
| **QS-TMP-1** | **Temporal Action-cost spike** (blocks unconditional ADR-0014; fallback = hand-rolled saga) | **open — costed spike; flagged to human (Phase-16 review)** |
| QS-2 | Saga vs Temporal | ✅ ADR-0014 (Temporal, gated) |
| QS-3 | DLQ/retry/outbox design + diagrams | ✅ `queue-flow.mmd` + `retry-flow.mmd` |
| QS-4 | Whale-tenant HOL mitigation | Temporal fairness (or partition routing/whale topic if fallback) |

## 6. Reviewer result (`/gate-check` Phase 10)
| Check | Result |
|-------|--------|
| Tradeoffs compared (not listed) + cited | PASS (§2; research cited) |
| Concrete defaults chosen + ADRs | PASS (ADR-0013/0014) |
| Lag back-pressure + checkpoint recovery | PASS |
| Every required pattern mapped to a realization | PASS (§4) |
| Diagrams ↔ prose | PASS (`queue-flow`/`retry-flow`/`event-flow`) |
| Cost/ops tension surfaced (Temporal vs ADR-0010) | PASS (ADR-0014; QS-TMP-1) |

**Gate:** `GATE-PASS` with **QS-TMP-1 flagged for the human** (Temporal cost-gate) — recorded, not blocking
the auto-advance since a safe fallback (hand-rolled saga) is documented.
