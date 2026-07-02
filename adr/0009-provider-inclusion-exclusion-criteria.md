# ADR 0009 — Provider inclusion/exclusion criteria (API-first vs. data provenance)

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Staff Security Engineer, Lead Enterprise Solutions Architect, Senior Product Manager
- **Phase:** 3 · **Source:** `docs/03-Provider-Research.md` §6

## Context
During Phase 3, research agents flagged several providers (Coresignal, Kaspr, ContactOut) `EXCLUDED`
because their datasets are built via **public-web data collection / LinkedIn-derived data**. But ADR-0002
("our engine is API-first, no scraping") is a constraint on **how *we* acquire data** (we only call
provider APIs) — not a claim that a provider's *supply chain* never touched public-web data. Applying
"scraped provenance ⇒ EXCLUDED" uniformly is **inconsistent**: Apollo (Phase 1, ACTIVE) self-describes
ingesting "230M+ records scraped/ingested monthly", and ZoomInfo fuses "38M+ online sources." A rule
that excludes Coresignal but includes Apollo is incoherent and would eliminate nearly every aggregator.

## Options considered
| Option | Pros | Cons | Tradeoff |
|--------|------|------|----------|
| "Any scraped provenance ⇒ EXCLUDE" | simple, maximally cautious | inconsistent (would exclude Apollo/ZoomInfo too); guts coverage | purity vs coverage/consistency |
| "Ignore provenance; include anything with an API" | max coverage | ignores real legal/ToS/compliance risk | coverage vs risk |
| **Tiered criteria (chosen)** | consistent, risk-calibrated | requires per-provider judgment + a compliance review | balanced |

## Decision
Classify each provider by **how we integrate** and **enforceable legal/continuity risk**, not by whether
public-web data exists anywhere upstream:

- **EXCLUDED (hard)** iff any of: (a) integration would require **us** to scrape / there is **no
  bona-fide server-side API**; (b) an **active legal/ToS prohibition** makes API use unlawful or
  structurally unstable (e.g. LinkedIn-derived data under active litigation); (c) the product is
  **defunct/absorbed** with no viable standalone API (continuity).
- **DEPRIORITIZED (compliance-gated, usable)** iff: the provider has a **legitimate licensed API** but
  its provenance includes public-web/LinkedIn-derived data → allowed only behind a compliance review
  (data-broker registration, consent/suppression, DPA), routed after cleaner-provenance sources, with
  provenance persisted. This is the **same class as Apollo/ZoomInfo** (which remain ACTIVE) — so the
  label reflects *incremental* risk, not a hard bar.
- **ACTIVE-CANDIDATE** otherwise.

**Applied reclassifications (research-agent verdict → final):**
- Proxycurl/Nubela: EXCLUDED → **EXCLUDED** (LinkedIn litigation + service wind-down; legal+continuity).
- Datanyze: EXCLUDED → **EXCLUDED** (ZoomInfo-absorbed, no viable standalone API; continuity).
- Kaspr: EXCLUDED → **DEPRIORITIZED** (LinkedIn-extension provenance; compliance-gated) — *human policy confirmation pending*.
- ContactOut: EXCLUDED → **DEPRIORITIZED** (LinkedIn/crawl provenance) — *human policy confirmation pending*.
- Coresignal: EXCLUDED → **DEPRIORITIZED** (legitimate DaaS API; public-web provenance = same class as Apollo) — *human policy confirmation pending*.

## Rationale
Consistency + risk-calibration over blanket caution. The hard bar is *our* acquisition method and
*enforceable* legal/continuity risk; softer provenance risk is a **compliance gate**, not an exclusion.
This keeps the exclusion rule coherent with the already-accepted ACTIVE aggregators.

## Human decision (PR-EXCL-1) — resolved 2026-07-01
A human confirmed the **DEPRIORITIZED, compliance-gated** treatment for Kaspr, ContactOut, and
Coresignal (not hard-exclude). They remain off-by-default and require a compliance review + persisted
provenance before any production use. Proxycurl + Datanyze stay hard-EXCLUDED.

## Consequences
- Positive: coherent roster; SSRF/compliance controls (`18`) carry the provenance risk, not a blanket ban.
- Negative/accepted: DEPRIORITIZED providers need a per-provider compliance review before production
  (tracked in `18`); they are not routed to production by default.

## Verification
Compliance review per DEPRIORITIZED provider before production use; Gap-Analysis re-checks that the
EXCLUDE/DEPRIORITIZE rule is applied uniformly (no Apollo-vs-Coresignal double standard).
