# ADR 0028 — Research-dossier API + canonical-Field additions (composite JSON, one-value-per-Field preserved)

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** Lead Enterprise Solutions Architect, Senior Product Manager, Principal Backend Engineer, GTM Data Platform Architect
- **Phase:** R&I (Research & Intelligence) · **Extends:** ADR-0012, ADR-0023

## Context
The research product's headline API is **"give a domain, get a full Company intelligence Dossier,"**
CRM-ingestion-ready without post-processing. Two facts constrain the shape:

1. **The existing API is subject + wanted-Fields oriented** (`POST /v1/enrichments` returns per-Field
   values with provenance; ADR-0012). It is not a domain → composite-document endpoint.
2. **The Field model is strictly one normalized value per Field** (`field_versions`, `docs/06`;
   ADR-0023). A Dossier, by contrast, is deeply nested and multi-valued: `news[]`, `competitors[]`,
   `acquisitions[]`, `funding_rounds[]`, `partnerships[]`, `locations[]`, `ai_summary`, per-section
   confidence, source references. Forcing that into canonical Fields would break the one-value-per-Field
   invariant that the whole persistence + provenance model depends on.

Some Dossier data, however, *is* genuinely single-valued and belongs in the canonical vocabulary so it
flows through the normal waterfall + provenance path: a Company's Twitter/Facebook/GitHub/Crunchbase
URL, its stock ticker, and its total funding.

ADR-0023 already fixes the extension rule: **the Field vocabulary is extended DOC-FIRST** (docs/00 §7 →
`internal/domain/field.go` → `Valid()`), and adding a normalized-scalar Field is **not** a schema change.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. Force all Dossier data into canonical Fields | one storage model | **breaks one-value-per-Field**; multi-valued data can't be represented; explodes the vocabulary | uniformity vs a load-bearing invariant |
| B. Dossier as an opaque JSON blob only | simple; flexible | loses queryable provenance (G5); can't reuse the waterfall for the scalar parts; no CRM normalization guarantees | simplicity vs provenance/reuse |
| **C. Composite Dossier referencing canonical Fields + 6 new scalar Fields, DOC-FIRST (chosen)** | preserves one-value-per-Field; scalar data uses the waterfall; multi-valued data is a research-owned composite with queryable provenance rows | two homes for data (Fields vs Dossier) — a boundary rule is needed | correct modeling vs a boundary to hold |

## Decision
**The Dossier is a research-owned composite document, not an extension of the Field vocabulary.**

- **New endpoints** (reuse `internal/api` handlers + `internal/job` + HMAC `internal/webhook`;
  `Idempotency-Key` required per ADR-0012; `/v1` versioning):
  - `POST /v1/research` — body `{company_domain?, company_name?, linkedin_url?, work_email?, phone?,
    wanted_sections[]}`. Default → **`202 {job_id, status}`** (async). `?mode=sync` → `200` **capped-
    budget preview** (firmographics + company_profile only; intent shown as last-known or `pending`,
    never a blocking compute — ADR-0027).
  - `GET /v1/research/{id}` — job status + Dossier when ready (mirrors `GET /v1/enrichments/{id}`).
  - `GET /v1/dossiers/{domain}` — the latest stored Dossier for a Company.
  - Completion **webhook** (HMAC-signed, idempotent) — the existing `internal/webhook` signer.
- **Dossier schema** (research-owned; top-level): `company_profile`, `contact_profile`,
  `firmographics`, `technographics`, `hiring_signals[]`, `intent{ intent_score, intent_topics[],
  buying_signals[] }`, `news[]`, `competitors[]`, `ai_summary`, `ai_reasoning`, `search_keywords[]`,
  `crm_ready{ account, contact }`, `confidence{ overall, by_section }`,
  `provenance[]{ field, provider, source_type, cost, idem_key, confidence, retained_losers }`,
  `processing_log[]`, `metadata{ config_version }`, `data_freshness{ generated_at, last_updated }`.
  Full JSON schema + OpenAPI in `docs/research-intelligence/06` + `openapi-research.json`.
- **The boundary rule (load-bearing).** **Single-valued** data → canonical Field (waterfall +
  `field_versions`). **Multi-valued / relational** data (`competitors`, `acquisitions`,
  `funding_rounds`, `partnerships`, `locations`, `seo_keywords`, `campaigns`, `news`) → **Dossier-only**,
  never a Field.
- **Six new canonical scalar Fields (33 → 39), added DOC-FIRST** (docs/00 §7 → `field.go` const +
  `canonicalFields` map → `Valid()`): `twitter_url`, `facebook_url`, `github_url`, `crunchbase_url`,
  `company_ticker`, `total_funding_usd`. Each is genuinely single-valued; no schema change (ADR-0023).
  `funding_stage`/`company_revenue` stay as-is; funding *rounds*/investors enrich them in the Dossier.
- **Queryable provenance.** Alongside the JSONB `provenance[]`, `research_sources` holds one row per
  source reference (G5) so provenance is queryable, with `source_type ∈ {api, dataset, ai_inference}`
  — **AI-inferred values are never fused as high-confidence facts.**
- **Ownership + storage.** `internal/research` owns `research_runs`, `research_steps`,
  `research_dossiers`, `research_sources` (migration 0015; FORCE RLS, no BYPASSRLS). Background refresh
  re-enqueues via `internal/job` on a freshness TTL.
- **CRM-ready normalization.** `crm_ready.{account,contact}` is a normalized projection built by the
  research module so a CRM connector (ADR-0030, roadmap) can ingest it without transformation.

## Rationale
Option C is the only shape that satisfies the product ask **and** protects the one-value-per-Field
invariant. It reuses the waterfall + provenance for the scalar data that fits, keeps the rich composite
where it belongs, and makes provenance queryable so a Dossier is auditable field-by-field. We rejected
"everything is a Field" (breaks persistence) and "Dossier is an opaque blob" (loses G5 provenance and
the waterfall reuse). The DOC-FIRST 6-scalar addition follows ADR-0023 exactly, so it is a governed,
reviewable change rather than an ad-hoc schema drift.

## Consequences
- **Positive:** a single call returns a normalized, CRM-ready, fully-provenanced Dossier; scalar data
  benefits from the waterfall; multi-valued data is cleanly modeled; provenance is queryable per field.
- **Negative / accepted:** data has two homes (Field vs Dossier) governed by the boundary rule; the
  Dossier schema must be versioned and kept in OpenAPI parity. Accepted.
- **Follow-ups / new ADRs triggered:** ADR-0030 (CRM outbound consumes `crm_ready`); the `field.go`
  code addition lands at implementation (Slice 22), the DOC-FIRST registration is already in docs/00 §7.

## Verification
- **Invariant:** a test asserts no multi-valued Dossier object (`competitors`, `news`, …) ever writes a
  `field_versions` row; only the 39 canonical Fields do. `field.go` `Valid()` accepts exactly the 39.
- **Contract parity:** `openapi-research.json` matches the handler DTOs (parity test, the ADR-0012
  discipline); every `POST /v1/research` write carries `Idempotency-Key` (replay → same result).
- **Provenance:** every Dossier value has a `research_sources` row with `source_type`; `ai_inference`
  values are visibly distinguished and never counted as high-confidence sourced facts.
- **Gates:** G1 RLS on all `research_*`; G4 aggregate Dossier cost ceiling reserved before collection.
  Section coverage/latency numbers stay `UNVERIFIED` until measured (`docs/research-intelligence/10`).
