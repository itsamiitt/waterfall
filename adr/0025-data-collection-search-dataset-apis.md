# ADR 0025 — Data-collection: search-API and public-dataset providers; the returned-URL fetch boundary

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** Lead Enterprise Solutions Architect, Staff Security Engineer, Principal Backend Engineer, Senior Product Manager
- **Phase:** R&I (Research & Intelligence) · **Supersedes:** ADR-0002 · **Refines:** ADR-0009

## Context
The platform is expanding from field-level enrichment into **research & intelligence** (a domain →
full-Company **Dossier**; `docs/research-intelligence/`). Producing a Dossier requires two data
shapes the enrichment roster does not cover: **web-search discovery** (find the pages/entities that
mention a Company) and **public bulk datasets** (authoritative firmographics, filings, scholarly and
legal-entity records).

**ADR-0002** ("API-first only; no scraping / browser automation / manual workflows") is in force. Its
decision bans *us* from browser automation and DOM/page scraping of arbitrary sites; coverage gaps
are closed by adding provider **APIs**, not by scraping. The forces behind 0002 — bounded, idempotent,
costed, compliant, small SSRF surface — are unchanged and must be preserved.

The candidates under consideration are all **server-side JSON/Atom/CSV/JSONL APIs**, not pages we
render or scrape:
- **Search APIs:** Brave Search API (own crawl index), Serper and Tavily (Google-SERP-derived).
- **Public datasets:** Common Crawl **CDX index** (URL/host discovery), OpenAlex (scholarly),
  SEC EDGAR (US filings), GLEIF (legal-entity LEI), and government business registries already
  represented in the roster (`docs/03`).

Two frictions must be resolved: (1) 0002's blanket "no scraping" wording does not distinguish *us
scraping* from *us calling a vendor's search API whose supply chain crawled the web* — the same
distinction ADR-0009 already drew for aggregators (Apollo/ZoomInfo stay ACTIVE despite crawl-derived
supply chains); (2) a search API returns **URLs**, and the temptation is to then GET those URLs and
parse their HTML — which *is* scraping and must remain banned.

All per-provider pricing, rate-limit, and coverage numbers are `UNVERIFIED` here and are cited in
`docs/research-intelligence/01` and `07`.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. Keep 0002 strict — no search/dataset providers | zero new surface; nothing to reconcile | guts the research product; no discovery, no filings/registries | purity vs product viability |
| B. Supersede 0002 to permit first-party crawling | maximal reach | reintroduces every risk 0002 killed (legal/ToS, brittleness, unbounded latency, huge SSRF surface); breaks positioning and the five gates | reach vs the platform's core identity |
| **C. Admit search + public-dataset APIs as providers; hard-ban page fetch/DOM parse (chosen)** | keeps every 0002 force; unlocks discovery + datasets; consistent with ADR-0009 | requires a crisp, enforceable "no page scraping" boundary + per-provider compliance gating | a precise boundary vs a simple slogan |

## Decision
**Search APIs and public bulk-dataset APIs are legitimate server-side Providers and register in the
adapter registry (`registry.go`) exactly like every other adapter** — secret-free `HTTPAdapter`/
`AsyncHTTPAdapter` emitting an `AuthDescriptor`; the egress tier injects the credential; the SSRF
choke + host allow-list applies to every call.

**The returned-URL / page-fetch boundary (enforceable):**
1. An adapter may consume **only** a provider's **structured server-side API response**
   (JSON / Atom / CSV / JSONL). It may **never** issue a raw GET to an arbitrary page and run
   DOM selectors, headless-browser automation, or HTML parsing over page bodies.
2. A URL returned by a search API is **discovery only**. It may be resolved **only** by passing its
   host/identifier to **another registered Provider API** (e.g. a returned domain → GLEIF LEI lookup,
   a returned CIK → SEC EDGAR filing API). It is **never** fetched-and-parsed as a web page.
3. **Common Crawl is restricted to the CDX index API** (URL/host discovery). Parsing Common Crawl
   **WARC bodies** is archived-page-HTML extraction — **scraping-by-proxy** — and is **DEFERRED**;
   admitting it requires its own future ADR. Index-only for now.
4. Browser automation, headless browsers, and DOM/page scraping remain **permanently banned**
   (0002's core, restated and retained). **No first-party crawling.**

**Inclusion status is decided by ADR-0009, not auto-granted:**
- Clean own-index / authoritative-dataset providers (**Brave**, **OpenAlex**, **SEC EDGAR**,
  **GLEIF**, government registries) → **ACTIVE-CANDIDATE**.
- **Crawl-provenance search** (**Serper**, **Tavily** — Google-SERP-derived) → **DEPRIORITIZED**
  (compliance-gated, off by default, routed after cleaner sources), pending the ADR-0009 human-policy
  confirmation — the same treatment as Coresignal/ContactOut.

New provider **categories** (one slug each, no aliases): `search`, `dataset`, `news`, `llm`.
`providers.category` has no CHECK constraint, so no migration is required to introduce them.

## Rationale
Option C preserves every force that justified 0002 — each call remains a bounded, idempotent, costed,
SSRF-guarded API call, so the five gates still hold — while unlocking the discovery and dataset inputs
the research product needs. It is also **consistent with ADR-0009**: we already decided that a
provider's upstream crawl provenance is a *compliance gate*, not a hard bar; a search API is the same
case. The sharp part is the **boundary**: the failure mode is not "calling a search API," it is
"following its URLs into page scraping." By making the boundary structural (structured responses
only; returned URLs resolve only via another API; WARC bodies deferred) we get the coverage without
reopening the arbitrary-fetch surface. We chose a precise, testable boundary over both a purity ban
(Option A, kills the product) and a crawling free-for-all (Option B, kills the positioning).

## Consequences
- **Positive:** research discovery + authoritative datasets become available under the existing
  egress/gate machinery, with zero new Go dependency and zero new call path.
- **Negative / accepted:** WARC-body extraction (and anything needing arbitrary page content) stays
  out of reach until a future ADR; crawl-provenance search sits behind a compliance review before
  production. Accepted.
- **Follow-ups / new ADRs triggered:** ADR-0026 (LLM-as-egress-adapter — the LLM adapters that
  consume these search/dataset results); a future ADR if WARC-body extraction is ever justified; the
  ADR-0009 human-policy confirmation for Serper/Tavily (tracked in `docs/research-intelligence/07`).

## Verification
- **Gap-Analysis + Security Auditor** confirm no adapter issues a raw page GET or runs DOM/HTML
  parsing; every `search`/`dataset` adapter consumes only a structured API response, and any URL it
  returns is resolved solely via another registered Provider API (grep for HTML-parsing / headless
  imports = none; the Go backend stays stdlib-only, ADR-0016/0022).
- **SSRF test:** every new adapter's egress passes the host allow-list + dial-time IP guard; an
  RFC1918 / metadata-endpoint target is refused.
- **Inclusion audit (`/provider-audit`):** Serper/Tavily are `DEPRIORITIZED` and off by default;
  Brave/OpenAlex/EDGAR/GLEIF are `ACTIVE-CANDIDATE`; Common Crawl adapter is index-only (no WARC
  body access). Per-provider pricing/limits stay `UNVERIFIED` until cited in `docs/research-intelligence/07`.
