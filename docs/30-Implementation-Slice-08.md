# 30 — Implementation Slice 08: calibration + bandit routing (the learned components) (Go)

**Status:** `IMPLEMENTED` (tests green, closed-loop unit-verified) · **Owner:** Staff ML Platform Engineer · **Last updated:** 2026-07-01
**Builds on:** [`29`](29-Implementation-Slice-07.md) · **Canonical spec:** [`adr/0005`](../adr/0005-confidence-calibrate-then-fuse.md), [`adr/0008`](../adr/0008-adaptive-routing-thompson-guardrailed.md), [`04`](04-Data-Flow.md) · **Approved by:** human (2026-07-01)

> Adds the two *learned* pieces of the methodology — calibrated confidence and a
> Thompson-sampling router — under the governing invariant: **the model proposes, the
> deterministic gate disposes.** Calibration changes a *number*; the bandit changes an
> *order*. Neither can relax a bound (G3), overspend the ceiling (G4), drop provenance (G5),
> cross a tenant (G1), or skip the idempotency ledger (G2).

## 1. Calibration — `internal/calibrate` (`adr/0005-confidence-calibrate-then-fuse`)
Isotonic regression via **PAVA** (Pool-Adjacent-Violators): a monotonic, non-parametric map
`raw score → P(correct)` fitted per `(provider, field)`. Properties:
- **Fitted offline, applied deterministically** (config-as-data, docs/06) — no training in the
  hot path.
- **Opt-in per pair:** an uncalibrated `(provider, field)` (or a nil calibrator) is the
  identity, so calibration never silently changes untuned providers.
- **Applied before fusion** in the engine: the log-odds fuse and SPRT stop now operate on
  *calibrated* confidence. **Provenance keeps the RAW provider score** (G5 audit trail
  unchanged); the resolved `FieldValue.Confidence` reflects the calibrated-then-fused value.

`TestIsotonic_CorrectsOverconfidence` shows a provider that self-reports 0.9 but is right 2/5
of the time calibrating to ≈0.4; `TestCalibration_AppliedBeforeFusion` shows the resolved
value at ≈0.33 while `Prov.Confidence == 0.9`, and the field **not** marked target-met.

## 2. Bandit router — `internal/bandit` (`adr/0008-adaptive-routing-thompson-guardrailed`)
A Beta-posterior Thompson sampler per `(provider, field)`: `Update(provider, field, success)`
moves `Beta(1+succ, 1+fail)`; a per-plan `Scorer` **samples** the posterior to produce an
ordering estimate. Implemented dependency-free (Marsaglia-Tsang Gamma → Beta).
- **Conservative floor:** the score is `w·sample + (1-w)·prior` where `w = pulls/(pulls+5)`, so
  a barely-observed provider leans on its static prior — neither blindly trusted nor buried
  (`TestConservativeFloor_NoDataUsesPrior`: zero data ⇒ score == prior).
- **Reproducible routing:** each plan gets a scorer seeded from the record, so a replay of the
  same record reproduces the ordering (`TestScorer_Reproducible`; docs/04 §4).
- **Sampled once per provider per plan** (never inside the sort comparator), so the ordering is
  stable within a plan.

## 3. Router seam — `router.Scorer`
`Planner.WithScorer(s)` orders the cascade by `s.Score(provider, field, prior)/cost` instead
of the static prior. Default (nil) preserves the exact prior behavior (existing router tests
unchanged). The bandit satisfies `Scorer` **structurally** — `internal/bandit` does not import
`router`, so there is no cycle. `TestPlan_ScorerReordersCascade` proves the scorer flips the
static name-tiebreak order.

## 4. Closed loop — engine `WithCalibrator` / `WithBandit`
- The engine **updates** the bandit after each *real* provider call (`success` = the provider
  yielded the field). Cache hits (G2) do **not** update — no double-counting.
- `TestBandit_LearnsBetterProvider` drives 40 distinct records through a reliable provider and a
  failing one; the reliable provider's posterior mean overtakes the failing one
  (mean(good) > 0.6 > 0.5 > mean(bad)) — all while the gates run untouched.
- Wired into `cmd/enrichapi`: a fresh bandit-scored plan per request (per-request scorer avoids
  sharing a non-concurrent RNG across workers; seed derived from the record via FNV-1a).

## 5. Honestly out of this slice
- **No online/streaming calibration or auto-refit:** models are fitted offline; there is no
  job here that collects labels and re-fits. The label source (ground-truth feedback loop) is
  future work (docs/22).
- **No contextual bandit / no cost-aware Bayesian regret bound proof** — this is per-arm
  Thompson with a heuristic conservative blend, not the full Pandora/Gittins treatment
  (the reservation-value ordering remains the static-prior baseline).
- **Bandit state is in-memory & per-process** — not shared across replicas or persisted; a
  restart cold-starts the posteriors. Durable/shared learning is future work.
- **Calibrator carries no versioning here** — production would bind a calibration model to
  `config_version` (already in the G2 idempotency key) so a re-fit invalidates caches.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Calibration monotonic, opt-in, applied pre-fusion; provenance keeps raw score | PASS |
| Bandit learns (closed loop, 40 records) without touching G1–G5 | PASS |
| Conservative floor: no data ⇒ prior; routing reproducible from seed | PASS |
| Scorer stable within a plan (sampled once); default = static prior | PASS |
| No import cycle (bandit satisfies router.Scorer structurally) | PASS |
| `go build/vet/test/gofmt` clean; 82 tests (10 new) | PASS |
| Offline-only fit, in-memory state, versioning honestly deferred (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; proceeds to the next increment on request.
