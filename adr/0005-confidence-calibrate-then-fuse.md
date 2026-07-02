# ADR 0005 — Confidence: calibrate-then-fuse (log-odds Bayesian) with an SPRT stop gate

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** GTM Data Platform Architect, Distributed Systems Engineer
- **Phase:** 2 · **Source:** `docs/02-Waterfall-Research.md` §4.2

## Context
We must turn heterogeneous provider scores into one trustworthy `0..1` Confidence per Field, combine
corroborating providers, and decide when we are confident enough to **stop** paying for more calls.

## Options considered
| Option | Pros | Cons | Tradeoff |
|--------|------|------|----------|
| Raw noisy-OR on provider scores | trivial | uncalibrated → overconfident; ignores correlation | simplicity vs correctness |
| Dempster–Shafer / full Bayes nets | models conflict + ignorance natively | Zadeh conflict blow-up, costly, hard to audit | expressiveness vs cost/auditability |
| **Calibrate-then-fuse (chosen)** | interpretable, cheap, associative/idempotent, provenance-friendly | conditional-independence assumption | pay with weight caps + correlation discount |

## Decision
Two layers + a stop gate. **Layer 1 — calibration:** never fuse raw scores; learn a per-(provider,
field) calibrator (isotonic where ≥~1000 labels/cell, else Platt/Beta) mapping raw score → real
probability; a **global** (cross-tenant) calibrator set on provider-intrinsic behavior (respects G1;
larger samples). **Layer 2 — fusion:** sum **log-odds** weights with a prior (Bayesian/Naive-Bayes;
= noisy-OR for presence, = FS m/u for value-agreement); **cap** each provider's per-field weight and
apply a **provider-correlation discount** (many GTM providers resell the same upstream data).
**Gate — SPRT:** accumulate the log-odds sum in waterfall order; **STOP** when confidence ≥ target
threshold **OR** the G4 cost ceiling **OR** the G3 timeout is hit — confidence is one of three hard
stops, never the only one. **Reject** production Dempster–Shafer and raw noisy-OR; Weighted-Majority
is used only to *learn* trust weights offline, not as the confidence output.

## Rationale
Log-odds is associative + order-independent (idempotent re-runs), O(#providers), and every provider's
contribution is an auditable additive weight (G5). SPRT yields the minimal expected number of paid
calls to hit the SLA — the economic point of a waterfall. We accept the independence assumption and
pay for it explicitly (caps + correlation discount) rather than adopting costlier evidence theories.

## Consequences
- Positive: interpretable, cheap, SLA-mappable confidence; clean stop semantics jointly with G3/G4.
- Negative/accepted: needs labeled outcomes (WQ-1), a provider-correlation matrix (WQ-2), calibrator
  versioning + drift monitoring (reliability diagrams/ECE as an SLO), cold-start priors (WQ-3).

## Verification
Reliability diagrams + ECE per calibrator and on the fused output; realized error rate vs promised
target-confidence after SPRT (overshoot check).
