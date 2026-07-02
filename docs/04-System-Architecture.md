# 04 — System Architecture

**Status:** `IN-REVIEW` · **Owner:** Lead Enterprise Solutions Architect · **Last updated:** 2026-07-01
**Gated by:** [doc-consistency](../skills/doc-consistency/SKILL.md) · [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) · [Security Auditor](../agents/security-auditor.md) · `/architecture-review` + `/security-audit`

> **Provenance.** Design panel `wf_2099540b-a5f`: 3 architecture proposals (ops-simplicity vs
> microservices vs cost/latency-balance) scored by 3 adversarial judges (scale, security, ops). Winner
> = **hybrid modulith + elastic data-plane** (best cost/p95 balance while meeting scale + isolation),
> with hardening ideas grafted from the microservices proposal. Decision → **[ADR-0010](../adr/0010-architecture-style-modulith-dataplane.md)**.

## 1. Decision summary
**Style: a control-plane "modulith" + independently-scaled stateless data-plane, in regional cells.**
The sub-millisecond, tenant-scoped planning modules (enrichment-api, orchestrator, identity,
verification, intent, cost/billing, key-manager) run **in one process** (splitting them would add 5–7
network hops to every record's p95 for no scaling benefit). Only the parts whose load curve genuinely
diverges are separate deployables: the **execution-engine worker pool** (scales on IO concurrency), the
**egress-proxy fleet** (sole SSRF boundary + key custodian), **offline-learning jobs**, and the
**dashboard/admin API**. See [`diagrams/architecture.mmd`](../diagrams/architecture.mmd).

## 2. Component view
| Plane | Component | Deployable | Responsibility |
|-------|-----------|-----------|----------------|
| Edge | api-gateway | edge (LB/Envoy) | authN/Z; **tenant_id from verified principal only**; rate-limit; versioning |
| Control (modulith) | enrichment-api | modulith | sync intake + async job persistence |
| Control | orchestrator | modulith | Pandora reservation-value ordering + Thompson router (**proposes**) — ADR-0007/0008 |
| Control | identity-service | modulith | tiered resolution (ADR-0004) + cache/preview |
| Control | verification-engine | modulith | calibrate → log-odds fuse → SPRT (ADR-0005) |
| Control | intent-engine | modulith | intent/signal ingestion (`14`) |
| Control | cost/billing | modulith | cost ceiling (G4) + idempotency & cost ledger owners |
| Control | key-manager | modulith | provider key pools, rotation, health (`12`) |
| Data | execution-engine | **worker pool** | bounded G3/G4 gate, deterministic merge; provider-adapters as linked SPI library |
| Data | egress-proxy | **fleet** | SOLE SSRF boundary; FQDN allow-list + safe resolver; **provider keys injected here** |
| Data | offline-learning | **batch jobs** | recompute calibrators/reservation values/reliability weights/bandit posteriors → versioned config |
| Data | dashboard/admin API | **service** | read-mostly ops/tenant UI (`17`) on replica + ClickHouse |

Provider-adapters are a **linked library (SPI)** inside the workers (pure transform/normalization);
their sockets always exit via the egress-proxy. Adding a provider = an adapter + a declarative
allow-list/config change, **no core deploy**.

## 3. Request lifecycle (two paths, one planner library)
See [`diagrams/api-flow.mmd`](../diagrams/api-flow.mmd). The **boundary is request cardinality/mode, not
a service boundary** — both paths execute the *same* planner library, so behavior is identical and
there is one pipeline to test.

- **Sync (single record):** gateway (authN/Z, tenant context) → enrichment-api → orchestrator runs
  cache/preview → tiered identity → **G4 reserve** → Pandora order + Thompson propose → executes the
  bounded parallel-prefix/cascade via egress-proxy → calibrate/fuse/**SPRT stop** → deterministic merge
  + provenance write → returns. No queue on this path (every avoided hop is p95 saved). Target p95 ≈
  0.8–1.5 s given ~3.2 calls with a cheap parallel first tier + early SPRT stop.
- **Async (batch/bulk, or any record flagged/over sync budget):** enrichment-api persists job + job_items
  under RLS, enqueues per-record items to Kafka (**partition = tenant_id** for fairness/isolation, **hash
  = record-identity** for idempotent placement), returns `202 job_id`. The worker pool consumes at a rate
  gated by per-(provider, key-pool) concurrency budgets, runs the identical pipeline, writes results +
  provenance, emits webhooks via egress-proxy.

## 4. Cross-cutting correctness enforcement (structural, not by convention)
This is the heart of the design: each gate is enforced by **placement**, so no missing check can
silently bypass it. (Full checklists in [`waterfall-correctness`](../skills/waterfall-correctness/SKILL.md).)

- **G1 tenant isolation — two layers.** (a) tenant_id is extracted **only** from the authenticated
  principal at the gateway and bound as a **signed/immutable request context** (mTLS/SPIFFE or signed
  JWT) — never from the request body; (b) the **enforcement floor is Postgres `FORCE ROW LEVEL SECURITY`**
  with `SET LOCAL app.current_tenant` per transaction, and the hot-path DB role has **no `BYPASSRLS`**.
  Redis keys and Kafka partitions are tenant-namespaced; identity clusters + bandit posteriors keyed by
  tenant_id. For the **Postgres OLTP** system-of-record, isolation lives in the datastore so an app bug
  cannot cross tenants. The **ClickHouse analytics store lacks Postgres-equivalent RLS**, so it uses a
  **compensating control** (`06` §2, `18` §1): ClickHouse **row policies** bound to a per-connection tenant
  setting **plus** a query layer that injects a mandatory `tenant_id` predicate server-side from the
  principal, with a CI test asserting no analytics/dashboard query runs un-scoped. Operator cross-tenant
  analytics are an explicit, audited RBAC capability, not an app default.
- **G2 idempotency.** Key = `hash(tenant_id, record-identity, field, provider, params, config_version)`;
  a durable **Postgres ledger (unique constraint)** with a **Redis check-and-set fast path** is consulted
  **before every provider call**; replays return the stored result with no second paid call. Thompson RNG
  is **seeded from the idempotency key** so the routing choice itself reproduces; Kafka consumers dedupe on
  job_item id.
- **G3 bounded.** A per-call wrapper enforces hard timeout, max attempts, jittered backoff, and a
  per-(provider, key, region) **circuit breaker whose state lives in Redis** (shared across workers). The
  deterministic gate re-checks feasibility **before every call**, regardless of the router's proposal.
- **G4 cost ceiling before execution.** At plan time cost/billing sums committed provider costs, **truncates
  the Pandora tail so committed ≤ ceiling**, and **atomically reserves credits** (Redis decrement backed by
  Postgres) before the first call; the parallel-prefix fires only if its summed committed cost is under the
  ceiling; failed calls compensate/refund. No call leaves without a confirmed reservation.
- **G5 provenance.** The deterministic merge writes each accepted field **with** calibrated confidence +
  W3C PROV lineage + provider + cost + **config-versions** + fetched_at into `field_versions` (retaining
  losers) in the **same tenant-scoped transaction** as the value; persistence is reachable **only** through
  merge-then-write with a `NOT NULL` FK to the provenance row — a bare value cannot be stored.
- **SSRF.** The **egress-proxy is the ONLY component with an internet route**; control-plane + workers run
  in a **default-deny no-egress subnet**. Every provider/webhook socket goes proxy → FQDN allow-list →
  DNS-rebinding-safe resolver (resolve-once, pin IP, re-validate, block RFC1918/link-local/metadata
  `169.254.169.254`) → connect. **Provider keys are injected at the proxy** and never enter worker memory.
  The choke point is guaranteed by **network policy**, not app discipline.

## 5. Data & event flow
See [`diagrams/event-flow.mmd`](../diagrams/event-flow.mmd). Datastores (detailed in `06`):
- **PostgreSQL** — OLTP system-of-record; RLS-pool multi-tenancy; `field_versions` provenance; idempotency
  + cost ledgers; **config-as-versioned-data** (calibrators, reservation values, reliability weights, bandit
  priors — referenced by version in every provenance row for deterministic re-resolution).
- **Redis** — cache/preview, idempotency fast-path, atomic credit/cost reservation, breaker state,
  per-provider concurrency tokens, hot config copies.
- **Kafka/Redpanda** — async intake (partition=tenant_id), results/events, webhook-delivery, per-stage DLQ;
  consumer lag is the primary back-pressure + autoscale signal. (Engine choice ratified in `10`.)
- **ClickHouse** — analytics (api_logs, usage, provider_statistics, history) via CDC; bounds the ~1.38 TB/day
  estimate; feeds offline learning, billing rollups, and the dashboard. (Engine ratified in `06`.)

## 6. Hitting the throughput target (assumption, load-tested in `21`)
2,000 rec/s × 3.2 calls = **6,400 provider calls/s**; at ~350 ms mean (p50 proxy, refine with measured
mean `21`), Little's Law ⇒ ≈**2,240 concurrent in-flight calls** steady (consistent with `00` §4; size
fleet for 4–8k to absorb tail + 5,000 burst). **Worker model:** async/
event-loop or virtual-thread workers (each provider call is an await-point, not an OS thread) — ~40–80
stateless pods each holding a few hundred sockets. **Per-provider concurrency budgets:** a distributed
token-bucket/semaphore per (provider, key-pool) in Redis caps aggregate concurrency to each provider's real
limit; key-manager rotates across a key pool to raise the ceiling; the same budget is the back-pressure lever
feeding queue lag. **Egress-proxy** uses keep-alive/TLS connection pooling to amortize handshakes. Detailed
sizing → `11`; cost-per-record → `16`.

## 7. Trust boundaries & blast radius
Trust boundaries: client↔edge (authN/Z), edge↔modulith (signed tenant context), modulith/workers↔datastores
(RLS), workers↔egress-proxy↔internet (SSRF choke, default-deny). **Blast-radius/scale unit = a regional
cell** (shared-nothing); scale out by "stamping another cell", which also bounds data-residency scope
(`18`/`19`). The control-plane modulith's shared blast radius is mitigated by in-process bulkheads + resource
caps + the clean seams that allow a module to be strangled into its own service if its scaling diverges.

## 8. Key risks & mitigations
| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Provider tail latency / correlated outages inflate p95 + blow the in-flight budget; retry storms | breakers, hedging/backup-provider on timeout, bounded queues, budget-capped retries |
| R2 | Egress-proxy is a throughput + availability SPOF and an extra hop | HA, horizontally scaled, keep-alive pooled; monitored as the choke it is |
| R3 | RLS-pool: a missing `SET LOCAL` or stray `BYPASSRLS` leaks tenants | FORCE RLS, no bypass role on hot path, **CI negative-isolation test** |
| R4 | Modulith shared blast radius | in-proc bulkheads, resource caps, regional cells, extractable seams |
| R5 | Cross-worker cost-reservation races on a shared tenant balance | atomic Redis reservation + Postgres reconciliation + compensation on failure |
| R6 | Config-version skew breaks replay determinism | pin config-versions into every `field_versions` row |
| R7 | Multi-region residency + posterior-sync complexity | regional cells; non-PII-only cross-region learning (`18`) |

## 9. Open items
| ID | Item | Status |
|----|------|--------|
| SA-1 | Architecture diagram (was placeholder) | ✅ replaced (`architecture.mmd`) |
| SA-2 | Sync/async boundary + queue placement | ✅ decided (§3; ADR-0010) |
| SA-3 | Datastore engine + RLS-pool model | provisional here; **ratify in `06`** (ADR) |
| SA-4 | Queue engine (Kafka/Redpanda) | provisional here; **ratify in `10`** (ADR) |
| SA-5 | `dependencies.mmd` reflecting the modulith/data-plane split | pending (`05`) |

## 10. Reviewer result (`/gate-check` Phase 4)
| Check | Result |
|-------|--------|
| Style decision has ≥2 options + surfaced tradeoff + ADR | **PASS** (3-proposal panel, judge scores, ADR-0010) |
| G1–G5 + SSRF enforced **structurally** (evidenced, not asserted) | **PASS** (§4 placement-based) |
| Diagram ↔ prose parity | **PASS** (`architecture`/`api-flow`/`event-flow` match §2/§3/§5) |
| Glossary terms only / cross-refs resolve | **PASS** |
| Throughput restated as assumption + math | **PASS** (§6; load test `21`) |
| Back-propagated + logged | **PASS** (`05`/`06`/`10` pointers; `CHANGELOG`) |

**Gate recommendation:** `GATE-PASS`. Provisional engine choices (SA-3/SA-4) are explicitly deferred to
their phase ADRs. **Awaiting human approval to advance to Phase 5 (Microservices).**
