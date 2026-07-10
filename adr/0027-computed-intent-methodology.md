# ADR 0027 — Computed intent methodology (signal → decay → fuse → calibrate → guardrailed score)

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** GTM Data Platform Architect, Principal Backend Engineer, Staff ML Engineer, Senior Product Manager
- **Phase:** R&I (Research & Intelligence) · **Extends:** ADR-0005, ADR-0008 · **Supersedes framing of:** `docs/14-Intent-Engine.md`

## Context
`docs/14-Intent-Engine.md` designed intent as an **ingest** lane: pull third-party intent topics
(Bombora, 6sense, G2, HG Insights) and store them in `intent_data`. It has no code. The research
product now needs the platform to **compute its own intent** across ten classes — buying, hiring,
tech-replacement, ai-adoption, security-investment, cloud-migration, digital-transformation,
crm-replacement, outsourcing, marketing-investment — from signals it collects (job postings,
technographics deltas, funding, news, and LLM-extracted signals), with a **weighted score, a
confidence, and human-readable reasoning** per class (the master-prompt requirement).

Constraints that bind the method:
- **"Model proposes, deterministic gate disposes"** — an LLM (or any learned component) may *propose*
  a raw signal, but the score a customer sees must be produced by a **deterministic, auditable**
  pipeline.
- **ADR-0005** already gives the platform a calibrate-then-fuse machine (`internal/calibrate` isotonic;
  `internal/engine.fuseAgreeing` log-odds). **ADR-0008** gives a guardrailed-learning discipline.
  Reuse beats reinvention.
- Intent is a **separate lane** from per-Field enrichment: different key (Company/account, not Person),
  different cadence (async/batch), different freshness model (surge + decay). `docs/14` established this.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. Ingest-only (keep `docs/14` as-is) | simplest; no scoring to defend | doesn't meet the product ask (no *computed* multi-class intent, no reasoning) | scope vs effort |
| B. LLM emits the final intent score directly | flexible; little math | un-auditable; LLM-disposed spend/score violates the governing invariant; not reproducible | flexibility vs auditability |
| **C. Deterministic signal→decay→fuse→calibrate pipeline; LLM proposes raw signals only (chosen)** | auditable, explainable, reuses ADR-0005 machinery; learned parts stay guardrailed | requires a signal taxonomy + weight config + calibration labels | rigor vs a bit more modeling |

## Decision
A new module **`internal/intent`** (one-owner of the new tables) computes each class score by a
**deterministic** pipeline:

1. **Collect + normalize signals.** Each raw signal is `{class, type, magnitude, observed_at,
   provider, confidence, cost}`, sourced from existing/new adapters (job postings via
   `theirstack`/`predictleads`; technographics deltas via `builtwith`/`wappalyzer`/`hg-insights`;
   funding via firmographics; `search`/`news` adapters) **and** from LLM extraction. **LLM outputs are
   *proposed raw signals only*** — they enter the pipeline as one more signal with `source_type =
   ai_inference` and are **never** a final class score.
2. **Decay.** Per-class score `= Σ_i w[class,type] · magnitude_i · decay(age_i)`, with
   `decay = 2^(−age / halflife[type])` — the surge-plus-decay half-life idea from `docs/14`, freshness
   half-life per signal type.
3. **Fuse.** Corroborating signals of the **same class** are combined in **log-odds** via
   `internal/engine.fuseAgreeing` (ADR-0005), with the ADR-0005 per-source weight cap +
   correlation discount so correlated providers don't double-count.
4. **Calibrate.** The fused score is mapped to a real probability by `internal/calibrate` isotonic
   (score → observed conversion), backfilled by the offline-learning job. **Confidence (G5) is
   attached per signal *and* per class score.**
5. **Explain.** Each `intent_scores` row stores a `reasoning` JSONB — the ordered per-signal
   contributions (type, raw magnitude, decayed value, weight, provider, cost) — the auditable "why."

- **Weights are versioned config, not code and not a new table:** a `config_versions` kind
  **`intent_weights`** via `internal/dash/configver`; refresh jobs **pin** `config_version_id` exactly
  as enrichment jobs pin config (ADR-0006), so a re-score is reproducible against the weights that
  produced it.
- **Async-only lane.** A new `job.Kind = "intent_refresh"` keyed on `company_domain`/account, on the
  existing `internal/job` + `internal/pgoutbox` path; triggers = scheduled sweep + provider webhook
  (surging accounts) preferred over polling. **Intent never runs on the sync per-Field enrichment
  path** — a synchronous Dossier preview shows last-known intent or `pending`, never a blocking compute.
- **Single write-back owner.** `internal/intent` is the **only** writer of the canonical
  `intent_score` / `intent_topics` / `buying_signal` Fields into `field_versions` (G5 provenance),
  reconciling the computed engine with the existing Field vocabulary. The per-class breakdown stays in
  `intent_scores` (exposed via the intent API), **not** overloaded onto the single-valued Fields.
- **Relationship to `docs/14`.** `intent_data` remains the third-party **ingest** store; the computed
  engine adds `intent_signals` + `intent_scores` (migration 0016). This ADR **supersedes the framing**
  of `docs/14` (ingest-only) — the authority becomes `docs/research-intelligence/05`.

## Rationale
Option C meets the product requirement (multi-class computed intent + confidence + reasoning) while
keeping the customer-visible number **deterministic and auditable**, which the governing invariant
demands. It reuses the ADR-0005 calibrate-then-fuse machinery rather than inventing a second scoring
system, and keeps learned/LLM components in the *propose* role only. We rejected ingest-only (Option A,
misses the ask) and LLM-emits-the-score (Option B) because an LLM-disposed score is neither auditable
nor reproducible and would violate "model proposes, gate disposes."

## Consequences
- **Positive:** explainable, reproducible intent per class; reuses calibration/fusion; correlated
  providers discounted; freshness modeled; clean separation of ingest vs computed.
- **Negative / accepted:** needs a signal taxonomy + weight defaults + calibration labels (cold-start
  from provenance, tuned by offline-learning); per-class scores add a table (`intent_scores`) rather
  than living in the Field projection. Accepted.
- **Follow-ups / new ADRs triggered:** ADR-0026 (LLM adapters that propose signals); migration 0016
  (`intent_signals` partitioned + `intent_scores`); calibration-label sourcing tracked in
  `docs/research-intelligence/05`.

## Verification
- **Determinism/replay:** re-running `intent_refresh` for an account against a pinned
  `config_version_id` reproduces the same class scores byte-for-byte from the same signals.
- **Explainability:** every `intent_scores` row has a `reasoning` JSONB whose contributions sum
  (in log-odds) to the stored fused score.
- **Invariant:** an LLM-proposed signal never appears as a final class score without passing decay →
  fuse → calibrate; test asserts an `ai_inference` signal is fused/calibrated, not written through.
- **Gates:** G1 RLS on `intent_signals`/`intent_scores` (parent **and** partitions); G2/G3/G4/G5 on
  every signal-provider and LLM call (egress-proxy only). Scoring-quality numbers stay `UNVERIFIED`
  until backtested (`docs/research-intelligence/05`).
