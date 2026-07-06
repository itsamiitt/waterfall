# 22. Per-store adapter dependency policy for the design-target backends

Date: 2026-07-06

## Status

Accepted

## Context

The dashboard ships with Postgres-backed implementations behind Go interfaces for capabilities that,
at scale, are expected to move to purpose-built backends: Redis (SSE fan-out / hot KV), ClickHouse
(analytics rollups), Kafka/Redpanda (distributed transport, ADR-0013), Temporal (durable
orchestration, ADR-0014), and an S3 Object-Lock WORM store (audit anchor, doc 05 SEC-2). The repo is
deliberately **stdlib-only** (ADR-0016 confines the sole dependency exception to the frontend). Each
backend adapter therefore forces a choice: hand-roll a stdlib client (as the Postgres wire client
and JWT/Prometheus/bandit were hand-rolled), or take an ADR-0016 dependency exception for a
third-party client. We need a per-backend policy so this is decided once, consistently, rather than
ad hoc when each adapter is built.

## Decision

Classify each design-target backend by protocol complexity:

- **Hand-roll in stdlib (no dependency, no ADR-0016 exception):**
  - **Redis** — the RESP protocol is small and text-framed; a stdlib TCP client mirrors
    `internal/pg`. A `realtime.Source` + KV over Redis is buildable without a driver.
  - **S3 Object-Lock WORM** — S3 is HTTP + SigV4; the nightly audit-anchor export is a small stdlib
    `net/http` + `crypto/hmac` signer. No SDK.
- **ClickHouse — prefer the stdlib HTTP interface** (ClickHouse speaks HTTP + SQL), so the analytics
  `RollupStore` adapter can also be hand-rolled; a native-protocol driver is **not** adopted. If a
  future need forces the native protocol, that is a separate ADR-0016 exception request.
- **Requires an ADR-0016 dependency exception (documented, gated at adoption):**
  - **Kafka/Redpanda** — the wire protocol is large and versioned; a maintained client is warranted.
  - **Temporal** — the SDK is the practical way to use durable execution; hand-rolling is out of
    scope. Adoption is already cost-gated by QS-TMP-1 (ADR-0014).

No backend code is written until the backend is provisioned in a real environment; each adapter is
introduced behind its existing interface with a dual-run / shadow-read cutover and a **measured
trigger** (documented in `docs/waterfall-dashboard/15` Part 2) — never speculatively.

## Options considered

- **Per-backend classification (chosen).** Matches the repo's existing hand-rolled ethos where the
  protocol is tractable (Redis, S3, ClickHouse-HTTP) and reserves dependency exceptions for the two
  backends where hand-rolling is genuinely impractical (Kafka, Temporal). Keeps the stdlib-only
  guarantee maximally intact.
- **Blanket dependency exception for all adapters.** Rejected: would pull in four heavy clients,
  erode the auditability value of the stdlib-only rule, and is unnecessary for Redis/S3/ClickHouse.
- **Hand-roll everything.** Rejected: hand-rolling a correct Kafka or Temporal client is a
  disproportionate, error-prone effort with no auditability payoff.

## Consequences

- Redis, S3 WORM, and ClickHouse (HTTP) adapters, when built, add **no** third-party dependency.
- Kafka and Temporal adapters, if/when their triggers fire, each land with a focused ADR-0016
  amendment naming the exact client, pinned version, and supply-chain mitigations — decided at
  adoption, not now.
- The plan-only Part-2 sections in doc 15 record this classification per backend so the choice is
  not relitigated when each adapter is implemented.
