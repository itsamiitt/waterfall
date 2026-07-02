# ADR 0008 — Adaptive routing: Thompson sampling inside a deterministic G3/G4 gate ("model proposes, gate disposes")

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Distributed Systems Engineer, Staff Security Engineer, Cost/Scale Reviewer
- **Phase:** 2 · **Source:** `docs/02-Waterfall-Research.md` §4.5

## Context
Provider quality drifts and varies by field/region/segment. A learned router can cut cost and raise
hit-rate, but every bandit/budget safety guarantee in the literature is **soft/asymptotic** and can
transiently overshoot — unacceptable when the G4 cost ceiling and G3 timeout are **hard contracts**.

## Options considered
| Option | Pros | Cons | Tradeoff |
|--------|------|------|----------|
| Static rules only | deterministic, simple | no adaptation to drift/cold-start | stability vs adaptivity |
| Black-box contextual/neural bandit self-limiting on budget | statistically optimal | opaque, soft safety, hard to isolate per tenant | optimality vs auditability/hard-safety |
| **Thompson (segmented) inside a deterministic feasibility gate (chosen)** | adapts + interpretable + hard-safe | more components | adaptivity **with** hard guarantees |

## Decision
**"Model proposes, deterministic gate disposes."** (1) **Primary policy:** per-(provider, field,
region) **Thompson sampling** (Beta-Bernoulli), cold-started from our G5 provenance logs; posterior
state keyed by `tenant_id` (G1); hierarchical priors share learning **only** for non-PII firmographics;
sampler RNG **seeded from the idempotency key** so replays reproduce the choice (G2). (2) **Cost-aware:**
rank by expected **value-per-dollar** (`reward − λ·cost`, Bandits-with-Knapsacks), not raw hit-rate.
(3) **Non-stationarity:** discounted/sliding-window posteriors; a G3 circuit-breaker removes an
unhealthy arm; EXP3 is the worst-case fallback. (4) **Context:** start **segmented** (field × region ×
company-size bucket); graduate a field to LinUCB only when segmentation is measurably too coarse, and
gate every promotion behind **offline replay** on historical logs (never pay live to validate).
(5) **Hard guardrails are invariants, not learned:** the bandit emits only a **preference ranking**;
the Execution Engine independently re-checks G3 (timeout/attempts/breaker/jittered backoff) and G4
(ceiling + credit reservation) **before every call** and drops any infeasible provider. (6)
**Conservative-Bandit floor:** the learned policy is constrained to **never underperform the incumbent
static rule-based waterfall** (ADR-0007 baseline), so shipping learning can only help.

## Rationale
Thompson is empirically top-tier and self-tuning; putting it *inside* a deterministic feasibility gate
gives adaptivity without trusting a soft guarantee to honor a hard contract. Every pull's provenance
row *is* both the audit record and the reward signal, closing the historical-learning loop.

## Consequences
- Positive: cost/quality adaptation, per-tenant isolation, replay-deterministic, audit-friendly.
- Negative/accepted: reward definition (WQ-8), replay-eval fidelity, multiple-play/parallel-stop bandit
  extension (WQ-9), drift-window horizon (needs `03` benchmarking), cross-tenant sharing policy (WQ-6).

## Verification
Offline replay before any promotion; Conservative-floor regression test (never worse than static
baseline); G3/G4 gate rejects infeasible top-ranked providers in tests; replay determinism via seeded RNG.
