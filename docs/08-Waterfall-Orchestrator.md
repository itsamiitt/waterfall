# 08 — Waterfall Orchestrator (Adaptive Routing & Planning)

**Status:** `IN-REVIEW` · **Owner:** GTM Data Platform Architect + Distributed Systems Engineer · **Last updated:** 2026-07-01
**Gated by:** [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) · `/architecture-review` + `/scale-check`

> Decisions: ordering **[ADR-0007](../adr/0007-provider-ordering-pandora-cascade.md)**, routing
> **[ADR-0008](../adr/0008-adaptive-routing-thompson-guardrailed.md)**, stop **[ADR-0005](../adr/0005-confidence-calibrate-then-fuse.md)**.
> Seed per-field ordering: [`03` §3](03-Provider-Research.md). Pipeline: [`enrichment-pipeline.mmd`](../diagrams/enrichment-pipeline.mmd).

## 1. Role
Turn an `EnrichmentRequest{tenant_id, record, fields[], profile}` into an `ExecutionPlan` — which
Providers, in what order, parallel/sequential/skip, with per-step deadlines, cost caps, and stop
conditions — then hand it to the Execution Engine (`09`). The orchestrator **plans**; it does not call
providers.

## 2. Routing inputs (per candidate (provider, field, region))
From `provider_statistics` + config-as-data (`06`): historical **success rate**, **cost** (per match),
**latency** p50/p95 (measured), **calibrated confidence** contribution, **freshness**, **regional fit**,
tenant **priority profile**, **compliance constraints** (DEPRIORITIZED providers off by default, ADR-0009),
live **health** (breaker state), and **correlation/copy** group (ADR-0006 copy-discount).

## 3. Plan construction
1. **Cache/preview + dedup** (cheap discovery before paid reveal; `16`).
2. **Reservation-value index (ADR-0007):** offline-precomputed per (tenant, field) from cost + historical
   value; sort candidates by descending reservation value → the sequential order.
3. **Thompson router (ADR-0008) proposes** a re-ranking by expected **value-per-dollar** (`reward − λ·cost`),
   per (provider, field, region). It only re-orders/skips; it cannot breach gates.
4. **Skip** any provider whose reservation value < best value already obtainable.
5. **Parallel prefix:** fire the top-k cheapest **only** if combined committed cost is under the G4 ceiling
   and summed marginal value beats waiting (cost↔latency tradeoff, bounded).
6. **G4 pre-flight (ADR-0010):** sum committed costs, **truncate the plan tail** so committed ≤ ceiling;
   reserve credits before emitting the plan.
7. Emit `ExecutionPlan{steps[]{provider, field_set, mode(parallel|seq), deadline, max_cost, reservation_value}}`.

## 4. Decision logic (the questions the prompt requires answered)
| Question | Rule |
|----------|------|
| Which first? | highest reservation value / value-per-$ for the field+region |
| Which parallel? | bounded cheap prefix under the ceiling |
| Which sequential? | the ordered tail after the parallel prefix |
| Which skip? | reservation value < best-in-hand, or DEPRIORITIZED/EXCLUDED, or breaker-open |
| Highest confidence? | calibrated per-(provider,field) contribution (ADR-0005) |
| Lowest cost / highest historical success? | from `provider_statistics` |
| Retry? | transient/idempotent only, capped (G3) |
| Fall back? | on NOT_FOUND / TRANSIENT / breaker-open → next ordered step |
| **Stop?** | SPRT confidence target **OR** G4 ceiling **OR** G3 timeout (three hard stops) |

## 5. Scoring, profiles, learning
- **Confidence + provider scoring:** calibrate → log-odds fuse (ADR-0005); provider scores feed reservation
  values + Thompson posteriors.
- **Profiles:** provider **priority profiles**, **customer-specific** routing, **regional** routing — per
  tenant, expressed as overrides on the seed ordering (`03` §3).
- **AI-assisted routing guardrail:** "model proposes, deterministic gate disposes" — the Execution Engine
  re-checks G3/G4 before every call; **Conservative-Bandit floor** guarantees the learned policy is never
  worse than the static seed ordering (ADR-0008).
- **Historical learning + benchmarking:** every provider pull's provenance row (G5) is the reward signal;
  offline jobs recompute reservation values, reliability weights, calibrators, and posteriors → versioned
  config (deterministic re-resolution).

## 6. Interfaces
- In: `EnrichmentRequest{tenant_id, record, fields[], profile}`.
- Out: `ExecutionPlan{steps[]{provider, field_set, mode, deadline, max_cost, reservation_value}}`.

## 7. Open items
| ID | Item | Status |
|----|------|--------|
| OR-1 | Routing inputs schema | ✅ §2 (backed by `06` `provider_statistics`) |
| OR-2 | AI-router guardrail | ✅ ADR-0008 |
| OR-3 | Stop conditions | ✅ ADR-0005 |
| OR-4 | Cold-start reservation values/priors | ✅ seed (`03` §3); refined by measured data |
| OR-5 | Correlated-provider discount (WQ-2) | copy-groups from `12`; applied in fusion (ADR-0006) |
| OR-6 | Multi-play/parallel-stop bandit extension (WQ-9) | open — parallel prefix is the interim rule |

## 8. Reviewer result (`/gate-check` Phase 8)
| Check | Result |
|-------|--------|
| Every routing question answered with a rule | PASS (§4) |
| G3/G4 re-checked before every call (not trusted to router) | PASS |
| Stop is joint (confidence OR ceiling OR timeout) | PASS |
| Learning has a Conservative floor vs baseline | PASS |
| Regional/customer profiles supported | PASS |
| Cross-refs/ADRs resolve | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
