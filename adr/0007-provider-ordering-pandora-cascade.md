# ADR 0007 — Provider ordering: Pandora/Weitzman reservation-value index cascade + SPRT stop

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Distributed Systems Engineer, Cost/Scale Reviewer
- **Phase:** 2 · **Source:** `docs/02-Waterfall-Research.md` §4.4

## Context
The orchestrator must decide, per Field, which provider to call first, which to skip, which to run in
parallel, and when to stop — minimizing expected spend to reach a confidence target, under a hard
pre-execution cost ceiling (G4) and bounded latency (G3).

## Options considered
| Option | Pros | Cons | Tradeoff |
|--------|------|------|----------|
| Learned RL acquisition / Gittins index | per-record cost-optimal | model-hungry, opaque, non-deterministic | optimality vs auditability/determinism |
| Naive static priority list | trivial, deterministic | ignores marginal value-vs-cost; leaves money on table | simplicity vs cost-optimality |
| **Pandora reservation-value index cascade (chosen)** | provably-good, deterministic, precomputable, auditable | assumes provider independence | near-optimal + enforceable |

## Decision
Adopt a **Weitzman/Pandora reservation-value index** as the default ordering-and-stopping brain,
packaged as a **Viola-Jones-style cheap-first cascade**, with a FrugalGPT-style per-field confidence
scorer as the accept/escalate/stop gate. (1) Offline, per (tenant, field), precompute each provider's
reservation index from its cost + historical value/hit-rate; store as static auditable config.
(2) At plan time, sort providers by descending reservation value; **skip** any whose reservation value
is below the best value already obtainable. (3) **Stop** when best-in-hand exceeds every remaining
provider's reservation value **OR** the SPRT confidence target is met (ADR-0005) — whichever first.
(4) **G4:** sum planned providers' costs *before* execution and truncate the tail so committed spend
never exceeds the ceiling. (5) **Parallelism** is a bounded **prefix** only: fire the top-k cheapest
together when combined committed cost is under the ceiling and summed marginal value beats waiting.
**Defer** learned RL / Gittins-quality-learning to v2 (needs labeled outcomes). The Thompson router
(ADR-0008) *proposes* the ranking on top of this deterministic frame.

## Rationale
Chose a provably-good, interpretable, deterministic policy we can precompute, cap under a hard budget,
and fully attribute in provenance, over marginally-more-optimal but opaque/non-deterministic policies.
Parallelism trades a little extra spend for latency but must never breach G4 — hence bounded prefix.

## Consequences
- Positive: deterministic, auditable, budget-enforceable ordering; minimal expected paid calls.
- Negative/accepted: cold-start reservation values (WQ-3), correlated-provider VOI discount (WQ-2),
  the parallel-prefix sizing rule, and a recall policy for revisiting skipped providers (idempotently).

## Verification
Committed-cost ≤ ceiling proof before execution; A/B vs naive priority list on cost-per-match + hit
rate; deterministic plan for identical inputs.
