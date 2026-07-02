# ADR 0006 — Conflict resolution / merge: deterministic online resolution + offline-learned weights + full PROV

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** GTM Data Platform Architect, Database Architect
- **Phase:** 2 · **Source:** `docs/02-Waterfall-Research.md` §4.3

## Context
When providers return different non-null values for a Field, one must win — deterministically,
bounded in latency (G3), within cost ceiling (G4), idempotently, and with full auditability. Naive
majority vote is *actively wrong* for us because GTM providers copy/resell overlapping upstream data,
so "more sources agree" can be an illusion.

## Options considered
| Option | Pros | Cons | Tradeoff |
|--------|------|------|----------|
| Live truth-discovery (TruthFinder/CRH/LTM/Dawid–Skene on request path) | adaptive, learns who to trust | iterative/sampling → unbounded latency, non-deterministic, hard to audit | accuracy/adaptivity vs determinism/bounded cost |
| Naive majority vote | trivial | double-counts copied sources → wrong | simplicity vs correctness |
| **Split estimation from resolution (chosen)** | adaptive offline + deterministic online | two pipelines to maintain | best of both |

## Decision
**Split the two.** **Online (request path):** a deterministic **reliability-weighted resolution**
function (Bleiholder–Naumann "deciding" functions with CRH-style weights): score each candidate value
by `source_weight × per-type agreement` (0/1 for categorical email/title; normalized closeness for
numeric headcount/revenue), apply **freshness decay** and a **per-tenant authority/trust order**, with
an explicit deterministic **tie-break** (highest weight → most recent → lexicographic). O(#candidates),
pure, idempotent, cheap. **Offline (batch, per-tenant, off request path):** TruthFinder/CRH re-estimate
per-(provider, field, region) reliability weights from **our measured** hit-rates; **Accu-style
copy/dependence detection discounts correlated providers**; LTM where multi-truth matters. **Never**
run these live. **Provenance (G5, non-negotiable):** model every field with **W3C PROV** — persist
**all** candidate values (winners *and* losers) with provider, fetch timestamp, provider-reported +
calibrated confidence, and the exact resolution rule/weights that fired. Losers are never discarded.

## Rationale
Determinism + idempotency + bounded latency are hard contracts on the request path; adaptivity is not.
Keeping losers enables audit, dispute resolution, and idempotent **re-resolution** under new weights.
Copy-discounting is a *correctness* requirement here, not an optimization.

## Consequences
- Positive: explainable "why this value won", replayable, compliance-friendly lineage.
- Negative/accepted: maintain the copy/reseller graph (WQ-2), freshness half-lives per field (WQ-5),
  multi-truth policy (WQ-4), weight-table versioning for deterministic re-resolution.

## Verification
Deterministic replay test (same inputs+weights → same winner); provenance completeness test (losers
retained); copy-discount reduces corroboration for known reseller pairs.
