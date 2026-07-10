# ADR 0002 — API-first only; no scraping / browser automation / manual workflows

- **Status:** Accepted · **Superseded by [ADR-0025](0025-data-collection-search-dataset-apis.md)** (2026-07-09) — 0025 narrowly restates and retains the no-scraping / no-browser-automation core, and additionally admits third-party **search APIs** and **public bulk-dataset APIs** as legitimate server-side Providers with a defined returned-URL fetch boundary. The Decision below is unchanged and remains in force except as extended by 0025.
- **Date:** 2026-06-30
- **Deciders:** Lead Enterprise Solutions Architect, Staff Security Engineer, Principal Backend Engineer
- **Phase:** 0

## Context
The engine must enrich thousands of Records/sec for many Tenants. Data can be acquired by (a)
provider APIs, (b) scraping/browser automation, or (c) manual workflows. Scraping introduces legal/
ToS risk, brittleness, anti-bot arms races, unbounded latency, and a large SSRF/abuse surface;
manual workflows cannot scale to the throughput target. The product requirement is explicitly
API-first.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. API-first only | Bounded, idempotent, monitorable, compliant, scalable | Limited to what providers expose; provider cost | Coverage-via-scraping vs reliability/compliance |
| B. APIs + scraping fallback | Higher raw coverage | Legal/ToS risk, brittle, unbounded latency, huge SSRF surface, hard to make idempotent | Coverage vs risk |
| C. APIs + manual ops | Coverage on hard cases | Cannot hit throughput; not API-first | Coverage vs scale |

## Decision
**API-first only.** All data acquisition is via provider APIs. No browser automation, no DOM/page
scraping of arbitrary sites, no manual/human-in-the-loop enrichment. Coverage gaps are addressed by
adding more provider APIs, not by scraping.

## Rationale
We deliberately trade some theoretical coverage for reliability, idempotency, bounded latency,
compliance, and a far smaller attack surface. This makes the five `waterfall-correctness` gates
achievable (you cannot make arbitrary scraping idempotent + bounded + cost-ceilinged cleanly).
Chose compliance/reliability over maximal coverage.

## Consequences
- Positive: every external call is a bounded, idempotent, costed API call → the correctness gates hold.
- Negative / accepted: some data only available via scraping is out of reach; we depend on provider
  coverage + pricing. Accepted.
- Follow-ups: SSRF controls still required because URL/domain **inputs** (company_domain, webhook
  callbacks) exist even without scraping — see `18-Security.md` + ADR for egress proxy (Phase 13/18).

## Verification
Gap-Analysis + Security Auditor verify no module performs scraping/automation; any "provider" that
requires it is `EXCLUDED` in `03` with a reason.
