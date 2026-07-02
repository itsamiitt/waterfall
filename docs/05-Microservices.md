# 05 — Microservices / Module Catalog

**Status:** `IN-REVIEW` · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Gated by:** [doc-consistency](../skills/doc-consistency/SKILL.md) · `/architecture-review`

> Style fixed by **[ADR-0010](../adr/0010-architecture-style-modulith-dataplane.md)**: a control-plane
> **modulith** (in-process modules) + independently-scaled **data-plane** deployables. This doc is the
> canonical catalog; the dependency graph is [`diagrams/dependencies.mmd`](../diagrams/dependencies.mmd).

## 1. Module/service catalog
Each entry: responsibility · exposed interface · **data ownership** (which tables it owns) · key deps.

### Control-plane modules (one deployable; in-process calls, one trace span tree)
| Module | Responsibility | Interface | Owns (tables) | Depends on |
|--------|----------------|-----------|---------------|-----------|
| **enrichment-api** | sync intake + async job persistence; request validation | REST (via gateway) | `enrichment_jobs`, `job_items` | orchestrator, Postgres, Kafka |
| **orchestrator** | plan build: Pandora ordering + Thompson propose (ADR-0007/0008) | `plan(req)→ExecutionPlan` | `reservation_values`, bandit posteriors | identity, cost, key-manager, config |
| **identity-service** | tiered resolution + dedup/merge keys + cache/preview (ADR-0004) | `resolve(record)→cluster_id` | `identity_clusters`, identity graph | Postgres, Redis |
| **verification-engine** | calibrate → log-odds fuse → SPRT (ADR-0005); email/phone verify orchestration | `score/verify(field)` | `calibrators`, `confidence_scores` | Postgres, workers |
| **intent-engine** | intent/signal ingestion + scoring/decay (`14`) | `signals(account)` | `intent_data` | workers, Kafka |
| **cost/billing** | cost ceiling (G4) + idempotency & cost ledgers; usage/credits | `reserve/reconcile`, `usage` | `credits`, `usage`, `billing`, `idempotency_ledger`, `cost_ledger` | Postgres, Redis, key-manager |
| **key-manager** | provider key pools, weighted routing, rotation, health, quotas (`12`) | `leaseKey(provider)` | `provider_keys`, `key_pools`, `provider_statistics` | Vault, Postgres |

### Data-plane services (separate deployables)
| Service | Responsibility | Scales on | Owns | Depends on |
|---------|----------------|-----------|------|-----------|
| **execution-engine workers** | run plans behind G3/G4 gate; deterministic merge; provider-adapters SPI. In the async lane, run as **Temporal workers** (Saga/checkpoint via workflow History — ADR-0014, cost-gated; fallback hand-rolled saga) | in-flight IO (Little's Law) | `provider_results`, writes `field_versions` | egress-proxy, Postgres, Redis, cost, Kafka/Temporal |
| **egress-proxy** | **sole** SSRF boundary; allow-list + safe resolver; **key injection** (`13`) | outbound rps | — (stateless) | key-manager (keys), providers |
| **offline-learning jobs** | recompute calibrators/reservation values/reliability weights/bandit posteriors → versioned config | batch | writes config-as-data | ClickHouse, Postgres |
| **dashboard/admin API** | operator + tenant-admin surface (`17`); RBAC/ABAC | read-mostly | — | Postgres replica, ClickHouse |

## 2. Module-boundary rules (so the modulith stays extractable)
1. **Clean seams:** every module exposes a typed interface; cross-module access is via that interface
   only (no reaching into another module's tables). Enforced by package boundaries + DB-schema ownership.
2. **One owner per table** (the "Owns" column). Others read via the owning module's interface, not direct SQL.
3. **No hidden network coupling:** control-plane modules call in-process; only data-plane crossings are
   real network calls (workers→egress, →datastores, →Kafka).
4. **Extraction path:** if a module's scaling diverges, it can be strangled into its own service without a
   rewrite (interface already exists) — the microservices escape hatch from ADR-0010.

## 3. Provider-adapters (SPI library, not a service)
The 46-provider roster (`03`) is a **linked library** inside the workers: each adapter implements the
`ProviderAdapter` contract ([api-integration](../skills/api-integration/SKILL.md)) — auth, retry+jitter,
breaker, timeout, error-taxonomy, idempotency, normalization to canonical Fields. Adding a provider =
new adapter + declarative allow-list/config change, **no core deploy**.

## 4. Open items
| ID | Item | Status |
|----|------|--------|
| MS-1 | Service list + ownership | ✅ this catalog |
| MS-2 | Modulith vs microservices | ✅ ADR-0010 |
| MS-3 | `dependencies.mmd` | ✅ matches this catalog |
| MS-4 | Exact table DDL + ownership | → `06` ERD |

## 5. Reviewer result (`/architecture-review` Phase 5)
| Check | Result |
|-------|--------|
| Every service has responsibility + interface + data owner + deps | PASS |
| Consistent with ADR-0010 topology | PASS |
| Diagram ↔ catalog parity (`dependencies.mmd`) | PASS |
| One-owner-per-table rule stated (feeds G1/G5) | PASS |
| Glossary/cross-refs | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
