# 00 — Project Overview

**Status:** `DRAFT` · **Owner:** Lead Enterprise Solutions Architect · **Last updated:** 2026-06-30

> This document is the project anchor. The **Glossary (§7)** is the single source of truth for
> entity names; per [`doc-consistency`](../skills/doc-consistency/SKILL.md) no entity may be named
> two different ways anywhere in the repo.

---

## 1. Problem statement

B2B go-to-market teams need accurate contact and company data (emails, phones, firmographics,
technographics, intent, etc.). No single data provider has full coverage or accuracy. A
**waterfall** strategy queries multiple providers in a cost- and confidence-optimized order until a
field is filled to a target confidence — maximizing coverage and accuracy while minimizing spend.

We are building an **enterprise-grade, API-first Waterfall Enrichment Engine** that any number of
tenants can call via API to enrich records at scale, with full provenance, cost control, and
compliance.

## 2. Scope

**In scope**
- API-first enrichment (REST + batch/bulk + async jobs + webhooks; gRPC/GraphQL evaluated in `07`).
- Multi-tenant isolation (row-level security; per-tenant keys, routing, billing, limits).
- Adaptive provider routing (cost / confidence / success-rate / regional / customer-specific).
- Confidence scoring, provenance, identity resolution, dedup, conflict resolution, merge.
- Provider key-pool management, health checks, quotas, rotation, failover.
- Email/phone verification engine; intent engine.
- Queue/worker architecture; retries, DLQ, idempotency, back-pressure, autoscaling.
- Observability, cost analytics, dashboard, RBAC/ABAC, audit, compliance, DR.

**Explicitly out of scope** (hard constraints)
- ❌ Browser automation, headless browsers, or DOM scraping.
- ❌ Web/page scraping of arbitrary sites.
- ❌ Manual/human-in-the-loop enrichment workflows.
- ❌ Any data acquisition path that is not a provider **API**.

Rationale recorded in [`adr/0002-api-first-no-scraping.md`](../adr/0002-api-first-no-scraping.md).

## 3. Primary users

| Persona | Need |
|---------|------|
| GTM / RevOps engineer (tenant) | Enrich CRM records via API/batch with cost + confidence controls |
| Platform operator (us) | Manage providers, keys, health, queues, billing across tenants |
| Tenant admin | RBAC, usage limits, billing, audit, regional routing |
| Data/ML engineer | Tune routing, scoring, benchmarks; consume enrichment history |

## 4. Throughput target (stated as a **tested assumption**, not a fact)

**Target:** sustained **2,000 enriched records/sec** steady-state, burst **5,000/sec**, per region.
This is an **assumption to be load-tested** (`21-Testing.md`), not a measured result. Supporting math:

- Assume mean **3.2 provider API calls per record** (waterfall depth; refined in `08`/`09`).
  → 2,000 rec/s × 3.2 = **6,400 provider calls/sec**.
- Assume mean provider latency **350 ms** p50 / **1,200 ms** p95 (per-provider figures are
  `UNVERIFIED` until `03-Provider-Research.md` cites them).
- Little's Law: in-flight calls ≈ throughput × latency = 6,400 × 0.35 s ≈ **2,240 concurrent calls**
  (p50); ≈ 7,680 at p95. Drives worker-pool sizing + per-provider concurrency budgets (`10`,`11`).
- At ~**8 KB** persisted per enriched record, 2,000/s ≈ **16 MB/s** write ≈ 1.38 TB/day raw before
  compression/retention (`06-Database-Architecture.md` defines partitioning + retention).
- Cost ceiling math (per-record spend, provider credit accounting) in `16-Cost-Optimization.md`.

> ⚠️ Every per-provider latency/throughput/credit number above is `UNVERIFIED` until cited in `03`.
> The aggregate target is an engineering assumption; the **gate to "VERIFIED" is a load test**.

## 5. Success criteria (definition of done for the platform)

1. Every planning doc complete, internally consistent, diagrams match prose + schema.
2. Tenant isolation provably enforced on every query (RLS + tests).
3. Every provider call: idempotent, bounded, timeout-wrapped, cost-checked, provenance-recorded.
4. SSRF-safe enrichment fetch path (the #1 security risk here) — see `18-Security.md`.
5. No unaccepted `FAIL` / `UNVERIFIED` items remain at any gate.

## 6. Highest-risk areas (prioritized)

1. **Tenant isolation** — a cross-tenant data leak is catastrophic. RLS + tests everywhere.
2. **SSRF in the enrichment fetch path** — provider calls + any URL/domain input must go through an
   egress allow-list + DNS-rebinding-safe resolver. See `18-Security.md` and
   [`waterfall-correctness`](../skills/waterfall-correctness/SKILL.md).
3. Cost runaway (uncapped provider spend) and provider-credit exhaustion.
4. Data residency / compliance (GDPR/CCPA) for PII across regions.

## 7. Glossary (CANONICAL — single source of truth)

> Update terms **only here**. Every other doc/diagram/schema must use exactly these names.

| Term | Definition | Do **not** call it |
|------|------------|--------------------|
| **Tenant** | An isolated customer organization using the platform. Carries `tenant_id`; RLS-scoped. | "account", "customer org", "workspace" |
| **Tenant Account** | A tenant's billing/subscription record. | "account" (ambiguous) |
| **Company** | An enriched **business entity** (the subject of enrichment). | "account", "org", "firm" |
| **Person** | An enriched **individual contact**. | "lead", "user", "contact record" |
| **User** | A human login belonging to a Tenant (RBAC principal). | "person" (that's enrichment data) |
| **Record** / **Subject** | The thing being enriched — a Person or a Company. | "row", "entity" (loosely) |
| **Field** / **Attribute** | A single enrichable datum, snake_case (e.g. `work_email`, `mobile_phone`). | "column", "property" |
| **Provider** | An external data vendor reached **only** via its API. | "source", "vendor API" loosely |
| **Provider Key** | An API credential for a Provider. | "token", "secret" loosely |
| **Key Pool** | A weighted set of Provider Keys for one Provider (rotation/failover). | "key group" |
| **Waterfall** | An ordered/conditional sequence of Provider calls to fill a Field set. | "cascade", "chain" |
| **Enrichment Job** | A unit of work enriching one or many Records. | "task", "request" loosely |
| **Adaptive Router** | Component choosing Provider order per Field/Record. | "dispatcher", "scheduler" |
| **Execution Engine** | Runs planned Provider calls (bounded, timeout, idempotent). | "runner", "executor" loosely |
| **Verification Engine** | Validates emails/phones. | "validator" |
| **Identity Resolution** | Merging Records that refer to the same real-world entity. | "matching" loosely |
| **Confidence** | A `0..1` score on a Field value. | "score" (ambiguous) |
| **Provenance** | Which Provider produced a Field value, when, at what cost, with what confidence. | "metadata" loosely |
| **Cost Ceiling** | Max spend per Record / Job / Tenant, enforced **before** execution. | "budget" loosely |
| **Idempotency Key** | Dedupe key making a Provider/external call exactly-once-effective. | "request id" loosely |

> **"Account" note:** in **ABM/intent** contexts the industry term "account" means a **Company** being
> targeted (account-level intent, account-based marketing). That usage is permitted **only** as
> "account-level" describing Company-scoped signals; the enrichment subject is still the **Company**, and
> "account" alone must never denote a Company elsewhere. The billing record remains **Tenant Account**.

Canonical Field vocabulary (extend only here): `work_email`, `personal_email`, `email_status`,
`mobile_phone`, `direct_dial`, `office_phone`, `phone_status`, `linkedin_url`, `job_title`,
`seniority`, `department`, `company_domain`, `company_name`, `employee_count`, `industry`,
`naics`, `sic`, `company_revenue`, `funding_stage`, `company_founded_year`, `company_hq_country`,
`company_hq_city`, `company_type`, `company_linkedin_url`, `company_phone`, `duns_number`,
`technographics`, `intent_topics`, `intent_score`, `buying_signal`, `first_name`, `last_name`,
`full_name` (name fields are person match keys / inputs for email-finder providers — added
Slice 04, `docs/26`).

> **Firmographics/technographics/intent Fields** (`company_revenue`, `funding_stage`,
> `company_founded_year`, `company_hq_country`, `company_hq_city`, `company_type`,
> `company_linkedin_url`, `company_phone`, `duns_number`, `technographics`, `intent_topics`,
> `intent_score`, `buying_signal`) back the L6–L8 providers of the 200-tool architecture
> (ADR-0023). `technographics` and `intent_topics` are inherently multi-valued but stored as a
> **single normalized Observation value** — a sorted, deduped, comma-joined list — so the
> one-value-per-Field `field_versions` model (`docs/06`) is unchanged.

## 8. Document map & phase plan

See [`README.md`](README.md) for the document index and [`IMPLEMENTATION_PROGRESS.md`](IMPLEMENTATION_PROGRESS.md)
for live status. Phases: **0 Tooling → 1 Market → 2 Waterfall → 3 Providers → 4–16 Architecture →
17–22 Ops/Product → Planning Completion Gate → Implementation → Post-impl reviews.**

## 9. Open items

| ID | Item | Status |
|----|------|--------|
| OV-1 | All per-provider performance/pricing numbers | `UNVERIFIED` until `03` |
| OV-2 | Throughput target validation | Pending load test (`21`) |
| OV-3 | Default cloud + region topology | Decided in ADRs during Phase `19` |
