# ADR 0010 — Architecture style: modulith control-plane + elastic stateless data-plane

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Lead Enterprise Solutions Architect, Principal Backend Engineer, Distributed Systems Engineer, Staff Security Engineer
- **Phase:** 4 · **Source:** `docs/04-System-Architecture.md` (design panel `wf_2099540b-a5f`)

## Context
We must choose an overall architecture style + topology that hits 2,000 rec/s (5,000 burst) per region,
enforces G1–G5 + SSRF structurally, and stays operable. A 3-proposal design panel (ops-simplicity vs
microservices vs cost/latency-balance) was scored by 3 adversarial judges (scale, security, ops).

## Options considered (panel results — sum over 3 judges, /30 per axis-set)
| Proposal | Style | Throughput | Isolation | Ops (5=simple) | Cost/rec (5=cheap) | Gate-fit | Verdict |
|----------|-------|-----------|-----------|----------------|--------------------|----------|---------|
| P3 cost/latency-balance | **hybrid modulith + data-plane** | 4 | 4 | 4 | **5** | 4 | **winner (25)** |
| P2 microservices | microservices | **5** | **5** | 2 | 2 | **5** | strong scale/sec, costly (23–24) |
| P1 ops-simplicity | modulith-heavy | 3 | 3–4 | **5** | 4 | 3–4 | simplest, weaker scale/isolation (22) |

## Decision
Adopt the **hybrid**: a single **control-plane "modulith"** (api-gateway edge, enrichment-api,
orchestrator, identity-service, verification-engine, intent-engine, cost/billing, key-manager as
in-process modules behind clean seams — they are lookup/CPU-bound, sub-ms, and share tenant-scoped
state, so splitting them only adds 5–7 hops to the p95 tail) **plus independently-scaled stateless
data-plane deployables** where scaling/blast-radius genuinely diverge: (a) **execution-engine worker
pool** (scales on IO concurrency, Little's Law); (b) **egress-proxy fleet** (the sole SSRF boundary +
provider-key custodian); (c) **offline-learning jobs**; (d) **dashboard/admin API** (read-mostly,
replica + analytics store). Provider-adapters are a linked SPI **library** in the workers; their
sockets always exit via the egress-proxy. **Regional cells** are the scale/blast-radius unit ("stamp
another cell").

**Grafted from the microservices proposal (judge-recommended):**
- **Two-layer G1 tenant identity:** tenant_id from a verified principal, cryptographically bound as a
  signed/immutable request context (mTLS/SPIFFE or signed JWT) **in addition to** Postgres FORCE RLS.
- **Provider keys injected AT the egress-proxy** — keys never enter worker/adapter memory.
- **Config-as-versioned-data:** calibrators, reservation values, reliability weights, and bandit
  posteriors are versioned rows, pinned into every provenance row (deterministic replay); Thompson RNG
  seeded from the idempotency key.
- **Per-stage dead-letter topics** for circuit-open/retry-exhausted items.

**Provisional (ratified in their phases):** Postgres RLS-**pool** multi-tenancy (db-per-tenant tier for
enterprise) → formal ADR in Phase 6; Kafka/Redpanda async boundary → formal ADR in Phase 10; ClickHouse
analytics store → Phase 6. Phase 4 commits to the *shape*; Phases 6/10 confirm the *engines* with full
tradeoff analysis.

## Rationale
Chose the best cost-per-record + p95 balance that still meets scale + isolation, over both the costlier
microservices topology (ops=2, cost=2) and the scale-weaker pure modulith. Co-locating the sub-ms
planning modules removes hops from the hot path; extracting only the IO-bound workers + the security
choke point puts elasticity exactly where load diverges. Clean module seams keep the microservices
escape hatch open (strangle a module out later without a rewrite).

## Consequences
- Positive: low hop count on the hot path, structural gate enforcement, ~4 operable units not 13,
  evolvable to microservices per-module.
- Negative/accepted: control-plane modulith has a shared blast radius (mitigate: in-proc bulkheads,
  resource caps, regional cells); egress-proxy is a throughput SPOF (mitigate: HA, horizontal, pooled).

## Verification
Load test to 2,000/5,000 rec/s (`21`); CI negative-isolation test (no BYPASSRLS on hot path); egress
default-deny network test (no component but the proxy can reach the internet).
