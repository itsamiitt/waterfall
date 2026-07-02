# ADR 0004 — Identity resolution: tiered deterministic → blocking → Fellegi–Sunter → cost-gated ML

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Distributed Systems Engineer, Database Architect, GTM Data Platform Architect
- **Phase:** 2 · **Source:** `docs/02-Waterfall-Research.md` §4.1

## Context
We must resolve a Person/Company across providers (by email, domain+name, LinkedIn) to dedupe,
merge, and mint stable golden-record IDs. A too-loose match in a multi-tenant system is a
**tenant-isolation breach** and corrupts golden records; a too-tight match loses recall.

## Options considered
| Option | Pros | Cons | Tradeoff |
|--------|------|------|----------|
| Deterministic-only | max precision, idempotent, ~0 cost, auditable | low recall (typos/aliases/missing keys) | precision vs recall |
| Probabilistic (Fellegi–Sunter) only | calibrated, label-free (EM), interpretable weights | threshold tuning; independence assumption | recall vs tuning risk |
| ML/DL matcher (Ditto) as default | SOTA F1 on dirty data | GPU cost/latency, black-box (weak G5), needs labels | accuracy vs cost/auditability |
| **Tiered (chosen)** | precision-first fast path + recall recovery + cost-gated ML | more moving parts | balanced |

## Decision
Adopt a **tiered resolver**: **T0** deterministic exact on normalized strong keys (idempotent) →
**T1** tenant-scoped blocking (LSH/MinHash, hard cap K candidates) → **T2** Fellegi–Sunter scoring
(EM-trained m/u, two thresholds, per-field weights persisted as provenance; Splink formulation) →
**T3** Ditto-style ML **only** for the ambiguous middle band, feature-flagged and gated behind G3
timeout + G4 cost ceiling. Managed resolvers (AWS Entity Resolution) may be a **fallback provider
node**, never the hot-path core. **Reject Zingg in-codebase** (AGPL-3.0 copyleft = IP risk).

## Rationale
Chose interpretability + idempotency + precision on the hot path; buy recall only when a cheaper tier
is ambiguous. FS per-field match weights double as G5 provenance ("email exact +8.2, name JW +3.1").
Match threshold favors precision by default (false-merge = isolation hazard). Correlated signals
(domain+company-name) are treated as one comparison to avoid double-counting (FS independence fix).

## Consequences
- Positive: bounded cost/latency (capped candidates), auditable merges, no labels required for T0–T2.
- Negative/accepted: threshold + m/u calibration + cluster-ID minting to design (see WQ-3/6/7/10).
- Follow-ups: `06` cluster/identity_graph schema + idempotent tenant-namespaced cluster IDs.

## Verification
Pairwise **and** cluster precision/recall/F1 per entity type (catch transitivity false-merges);
mandatory cross-tenant negative test (T1 blocking keys tenant-scoped).
