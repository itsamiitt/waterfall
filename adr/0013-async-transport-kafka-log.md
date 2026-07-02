# ADR 0013 — Async transport: Kafka-protocol partitioned log (Redpanda preferred)

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Distributed Systems Engineer, Staff DevOps Engineer, Cost/Scale Reviewer
- **Phase:** 10 · **Source:** `docs/10-Queue-System.md` (research `wf_2013b0cd-df8`, 7 techs cited)

## Context
The async batch/bulk lane needs: partition-by-tenant fairness, at-least-once + idempotency, back-pressure
**via consumer lag**, per-stage DLQ, retry queues, checkpoint recovery, and **multi-cloud portability** —
at ~6,400 provider-calls/s. Two requirements *eliminate* options: lag-based back-pressure and multi-cloud
portability.

## Options considered (cited, `10` §2)
| Tech | Verdict | Why |
|------|---------|-----|
| **Kafka-protocol log (Redpanda/Kafka)** | **chosen** | native partition-by-tenant, lag back-pressure, replay/offset checkpoint, managed on all clouds (MSK/Confluent/Aiven/Redpanda/Event Hubs) |
| Amazon SQS(+SNS) | rejected | best ops + native DLQ/redrive, but **AWS-only**, no log replay |
| Google Pub/Sub | rejected | great managed features (DLQ+backoff+seek), but **GCP-only** |
| RabbitMQ quorum queues | rejected as primary | back-pressure is **credit/confirm, not lag**; no log replay; no native partition-by-tenant |
| Redis Streams | secondary only | RAM-bound durability; async-replica can drop unacked on failover; build-it-all |
| NATS JetStream | strong runner-up | low ops, but partition-by-tenant needs client-side Orbit; managed = Synadia-only |
| Temporal | wrong category | it's orchestration, not transport (see ADR-0014) |

## Decision
Primary async engine = a **Kafka-protocol partitioned log**. **Redpanda preferred** for a small team
(single C++ binary, no JVM/ZooKeeper) or **managed Kafka** (MSK/Confluent/Aiven) to erase broker ops.
Do **not** choose on throughput (headline GB/s numbers are single-vendor + contested; both clear our load
with huge margin) — choose on **ops**. **Secondary store:** **Redis KV** as the idempotency/dedupe store
(not a second queue). **Accepted build-it-yourself gaps:** per-stage DLQ topics, delayed-retry topics,
priority topics (or push retry/priority/fairness into the orchestrator, ADR-0014).

## Rationale
Three of our hard requirements are "log-shaped" (partition-by-tenant, lag back-pressure, offset-replay
checkpointing) and the Kafka log gives them natively; multi-cloud portability via the Kafka protocol is
unmatched among *managed* options. We trade turnkey DLQ/retry (SQS/Pub/Sub) + rock-bottom ops for
portability + exact back-pressure/replay semantics.

## Consequences
- Positive: portable, exact lag back-pressure + replay, native tenant partitioning.
- Negative/accepted: build DLQ/delayed-retry/priority ourselves; a hot "whale" tenant sharing a partition
  causes head-of-line blocking (mitigate: high partition count, tenant→partition routing, whale topic —
  or Temporal Task-Queue fairness, ADR-0014).

## Verification
Load test to 2,000/5,000 rec/s on the chosen managed/self-hosted cluster (`21`); lag-driven autoscaling
test; DLQ + replay test.
