# 01 — Research Findings

> **Status:** DRAFT · **Owner:** Research & Competitive-Intelligence Lead · **Last updated:** 2026-07-09 · **Gated by:** /provider-audit, /architecture-review, /security-audit

> This document is the **cited research landscape** for the `docs/research-intelligence/` series: the
> competitor scan that motivated the platform verbs (Collect / Reason / Score / Deliver / Govern in
> [`00-overview.md §1`](00-overview.md)), the fact-checked profiles of the new data + LLM providers
> that [`03-data-collection.md`](03-data-collection.md) and [`07-provider-expansion.md`](07-provider-expansion.md)
> turn into registry rows, and a verification register. It **realizes the "all per-provider
> pricing/limits/coverage numbers are UNVERIFIED here, cited in `01`/`07`" pointer** in
> [ADR-0025](../../adr/0025-data-collection-search-dataset-apis.md) and
> [ADR-0026](../../adr/0026-llm-egress-adapter-cost-cascade.md). Nothing here re-decides a locked
> fact; it supplies the evidence behind the anchor's UNVERIFIED register (`00 §8`) and the ADR-0009
> inclusion verdicts. Every external claim is tagged **VERIFIED** (with a `source_url` +
> `verified_date`) or **UNVERIFIED**; no number is fabricated.

---

## 1. Method & scope

Two questions frame this scan. **(a) Product**: what do the incumbent research/intelligence platforms
do for research, intent, and AI, and which of their patterns should the platform adopt versus reject
given its locked architecture? **(b) Supply**: for each new data or LLM provider the research product
needs, what does it actually return, how does it authenticate, and does it fit the **no-scraping**
boundary ([ADR-0025](../../adr/0025-data-collection-search-dataset-apis.md))?

Sources are vendor documentation and pricing pages fetched **2026-07-09**. Facts that a vendor page
stated verbatim are **VERIFIED** with the URL; capability descriptions that rest on general product
knowledge or that a page did not confirm are **UNVERIFIED**. Per the repo discipline, this doc changes
no decision — it is the citation layer under the anchor's UNVERIFIED register (`00 §8`, RI-5).

## 2. Competitor scan — adopted vs rejected patterns

The platform positions in the class of ZoomInfo / Clay / Apollo / 6sense / Cognism / Common Room
(`00 §1`), but reuses the ~145-adapter Provider machinery, the RLS multi-tenancy, the durable
queue/worker fabric, and a **free-first LLM tier through the existing egress** rather than a new
dependency stack. Capability descriptions below are **UNVERIFIED** product-knowledge summaries (RI-5);
the value of the table is the **adopt / reject** column, each tied to a locked decision.

| Competitor | Research / intent / AI capability (UNVERIFIED) | Pattern we **adopt** | Pattern we **reject** (and why) |
|---|---|---|---|
| **ZoomInfo** | Large firmographic/contact DB; streaming + third-party **intent**; website-visitor de-anonymization; "Copilot" AI account summaries. | Multi-source **fusion** into one account view; surge-based intent; AI account summary as a **section**, not a fact. | A single proprietary scraped contact DB as the system of record — we are an **API-first aggregator** (ADR-0002/0025); their DB becomes *one* Provider, never the spine. |
| **Clay** | **Waterfall enrichment** across 100+ providers in a table UX; "Claygent" agentic web-research assistant; AI columns. | Waterfall-of-Providers (the platform's core Waterfall, `docs/00`) and an **AI research agent** pattern (agent task catalog, `04`). | **Claygent-style browser/agentic page scraping** — banned by ADR-0025; our agents consume only structured Provider-API responses, never fetched page DOM. |
| **Apollo** | Contact DB + sequencing/engagement; buying **intent** topics. Already a roster adapter (`apollo`, DEPRIORITIZED). | Unified account+contact enrichment feeding a single Dossier. | Reliance on a crawl-derived contact DB as a fact source; kept **DEPRIORITIZED** under the ADR-0009 gate, routed after cleaner sources. |
| **6sense** | Predictive **multi-class intent**, anonymous-account identification, AI recommendations. Already a roster adapter (`6sense`, intent). | **Multi-class computed intent** calibrated to observed conversion (ADR-0027) with surge+decay. | An **opaque black-box score** — the platform's intent is deterministic, auditable, and carries per-signal `reasoning` (ADR-0027; `05`). |
| **Cognism** | Contact DB, phone-verification ("Diamond") data, Bombora intent, GDPR-first posture. | **Verified-contact** signals + an explicit **compliance posture** (ADR-0009 status per provider; PII/DSAR baseline, `09`). | Proprietary-DB dependence; compliance handled as a **per-Provider gate**, not a marketing claim. |
| **Common Room** | Product-led / community + digital-footprint **signal capture**; person-level signals; "RoomieAI". | Wide **signal aggregation → scoring** from many typed sources feeding the intent pipeline. | Deep platform-integration **scraping** of community sites; we take only signals reachable via a registered Provider API. |

**Synthesis.** Every competitor pairs (1) broad data collection, (2) a computed intent/scoring layer,
and (3) an AI summarization/agent layer. The platform matches all three **without** the two things
that make incumbents expensive and legally exposed: a first-party crawler and a per-seat proprietary
database. Collection is API-only (ADR-0025), intent is deterministic and explainable (ADR-0027), and
the AI layer runs **free-first** as egress adapters (ADR-0026). The distinctive bet — "the model
proposes, a deterministic gate disposes" — is precisely what the black-box incumbents do *not* offer.

## 3. New data providers — cited facts

All values fetched **2026-07-09**. Auth-scheme column names map to the repo's `AuthDescriptor`
schemes (`internal/provider/egress.go`): `AuthAPIKeyHeader`, `AuthAPIKeyQuery`, `AuthBearer`, or
**none** (public dataset). "No-scraping fit" states how the source satisfies ADR-0025 (structured
server-side response only; any returned URL is discovery-only).

### 3.1 Search APIs (category `search`)

| Provider | Returns | Auth | Free tier / pricing | ADR-0009 status | No-scraping fit |
|---|---|---|---|---|---|
| **Brave Search API** | Web results from **Brave's own crawl index** (pages, locations, rich snippets). VERIFIED. | `X-Subscription-Token` header → `AuthAPIKeyHeader`. VERIFIED. | "Search" plan **$5 / 1,000 requests**, **$5 free monthly credits**, **50 queries/sec**. VERIFIED. | **ACTIVE-CANDIDATE** — clean own-index. | Structured JSON; returned URLs are **discovery-only**, resolved via another Provider API. |
| **Tavily** | Relevancy-ranked results (title, URL, snippet, score) + optional LLM answer/images/raw content. VERIFIED. | `Authorization: Bearer tvly-…` → `AuthBearer`. VERIFIED. | **1,000 free API credits/month**, no card; Basic Search **1 credit**, Advanced **2 credits**; PAYGO **$0.008/credit**. VERIFIED. | **DEPRIORITIZED** — LLM-search layer over SERP-class sources; compliance-gated. | Structured JSON; "raw content" is **not** consumed as page-DOM — index-only fields used, URLs discovery-only. |
| **Serper** | Google-SERP-derived structured JSON (organic, etc.), 1–2 s latency. VERIFIED. | `X-API-KEY` header → `AuthAPIKeyHeader`. VERIFIED. | **2,500 free credits** (6-mo expiry); packs from **$1.00/1K** down to **$0.30/1K** at scale. VERIFIED. | **DEPRIORITIZED** — Google-SERP provenance; off by default (same treatment as Coresignal/ContactOut). | Structured JSON only; returned URLs discovery-only, never fetched-and-parsed. |

### 3.2 Public datasets (category `dataset`)

| Provider | Returns | Auth | Free / pricing | ADR-0009 status | No-scraping fit |
|---|---|---|---|---|---|
| **Common Crawl CDX index** | Capture-index records (URL, host, timestamp, MIME, status) via the pywb **CDX/capture-index server**; `collinfo.json` lists monthly indices. VERIFIED. | **None** (public). VERIFIED. | Free public dataset. VERIFIED (no fee stated). | **ACTIVE-CANDIDATE — index-only.** | **Index API only** — URL/host discovery. Parsing Common Crawl **WARC bodies** is scraping-by-proxy and is **DEFERRED** (ADR-0025 §3; needs its own ADR). |
| **OpenAlex** | Scholarly entities: **works, authors, sources, institutions, topics, publishers, funders**. VERIFIED. | Free **API key** (query param) → `AuthAPIKeyQuery`; historical "polite pool" via `mailto`. Key VERIFIED; polite-pool UNVERIFIED. | **CC0**, free; docs note a **$1/day** free-usage allotment. VERIFIED. | **ACTIVE-CANDIDATE** — authoritative open dataset. | Structured JSON entities; no page fetch. |
| **SEC EDGAR** | US filings via `data.sec.gov`: **Submissions**, **Company Facts**, **Company Concept**, **Frames** (XBRL); JSON, near-real-time. VERIFIED. | **None / no API key.** VERIFIED. A **descriptive `User-Agent`** and the SEC fair-access rate cap apply (policy). | Free public API. VERIFIED. | **ACTIVE-CANDIDATE** — authoritative US-filings dataset. | Structured JSON; a returned **CIK** resolves via the EDGAR filing API, never a page GET. |
| **GLEIF** | **LEI records** + legal-entity & ownership data, BIC/ISIN mappings, corporate hierarchy, from the **Golden Copy**. VERIFIED. | Public API (`api.gleif.org`, JSON:API) → **none**. Public status VERIFIED; no-key detail UNVERIFIED. | Free public API. VERIFIED (no fee stated). | **ACTIVE-CANDIDATE** — already in the roster (`gleif`, firmographics). | Structured JSON:API; a returned **domain/LEI** resolves via another Provider API. |

> **Roster note.** GLEIF, OpenCorporates, and government business registries (Companies House, brreg,
> ARES-CZ, …) are **already registered** adapters (`internal/provider/adapters/registry.go`,
> firmographics). ADR-0025 cites them as the "authoritative-dataset" precedent; the genuinely **new**
> `dataset`-category adapters are **Common Crawl (index-only)**, **OpenAlex**, and **SEC EDGAR** (`07`).

## 4. LLM gateway landscape (category `llm`)

The AI layer is greenfield (ADR-0026 §Context): no LLM code exists in the repo today. The design bet
is an **OpenAI-compatible** gateway reached as an ordinary egress adapter, running **free-first**.

| Gateway | OpenAI-compatible | Auth | Free models | Free-model limits | Role in the cascade |
|---|---|---|---|---|---|
| **OpenRouter** | **Yes** — request/response schema "very similar to the OpenAI Chat API," normalized across providers. VERIFIED. | `Authorization: Bearer …` → `AuthBearer`. VERIFIED. | **Yes** — models with a **`:free`** suffix (e.g. `deepseek/deepseek-r1:free`, `meta-llama/llama-3.3-70b-instruct:free`); **28+** free models listed Jul 2026. VERIFIED. | **20 req/min**; **50 req/day** under $10 lifetime credit, **1,000 req/day** after $10. VERIFIED. | **Primary** (`openrouter`, free pool) → gated escalation to `openrouter-paid`. |
| **OpenAI (direct)** | Native. VERIFIED (by definition). | `AuthBearer`. VERIFIED. | No free-tier inference pool. | Paid per-token. | Paid escalation adapter (`openai`). |
| **Anthropic (direct)** | Messages API (not OpenAI-shaped; `x-api-key` + `anthropic-version` headers). VERIFIED. | `x-api-key` → `AuthAPIKeyHeader` (an integration detail the `AuthDescriptor` supports; ADR-0026 cites `AuthBearer` as the general example). VERIFIED. | No free-tier inference pool. | Paid per-token. | Paid escalation adapter (`anthropic`). |

The **deterministic free→mid→paid cascade** (ADR-0026) orders these by ADR-0007 reservation value;
the accept/escalate/stop decision is disposed by a gate over **schema-validity + budget + attempt-count
+ agreement**, never model self-confidence. Per-token pricing stays **UNVERIFIED** until measured in
[`11-cost-model.md`](11-cost-model.md) (RI-2).

## 5. Adopted vs rejected patterns — synthesis

| # | Pattern in the market | Decision | Anchor / ADR |
|---|---|---|---|
| 1 | Waterfall across many providers | **Adopt** — already the platform's core Waterfall. | `docs/00`; ADR-0002 |
| 2 | Agentic **browser** web research (Claygent-style) | **Reject** — no scraping/DOM/headless, ever. | ADR-0025 |
| 3 | Search + public datasets as inputs | **Adopt** — as `search`/`dataset` egress adapters. | ADR-0025 |
| 4 | Multi-class predictive intent | **Adopt** — but deterministic + explainable. | ADR-0027 |
| 5 | Black-box intent score | **Reject** — per-signal `reasoning`, reproducible. | ADR-0027 |
| 6 | AI account summaries | **Adopt** — as an `ai_inference` Dossier section, never a fused fact. | ADR-0026/0028 |
| 7 | LLM SDK / vector DB in-stack | **Reject** — zero new Go dep; RAG deferred. | ADR-0026/0029 |
| 8 | Model picks tools / spend | **Reject** — model proposes, gate disposes. | ADR-0026 |

## 6. Verification register

`verified_date` = **2026-07-09** for every VERIFIED row (fetch date). UNVERIFIED rows are design
targets or product-knowledge claims awaiting a cited source or a measurement gate (`00 §8`).

| # | Claim | Status | source_url |
|---|---|---|---|
| V-1 | Brave Search API uses Brave's own crawl index; auth header `X-Subscription-Token` | VERIFIED | https://api-dashboard.search.brave.com/app/documentation/web-search/get-started |
| V-2 | Brave "Search" plan: $5 / 1,000 requests, $5 free monthly credits, 50 qps | VERIFIED | https://brave.com/search/api/ |
| V-3 | Tavily returns relevancy-ranked results (+optional LLM answer); Bearer `tvly-` auth | VERIFIED | https://docs.tavily.com/documentation/api-reference/endpoint/search |
| V-4 | Tavily free tier 1,000 credits/month; Basic 1 / Advanced 2 credits; PAYGO $0.008/credit | VERIFIED | https://docs.tavily.com/documentation/api-credits |
| V-5 | Serper is Google-SERP-derived JSON; auth `X-API-KEY`; 2,500 free credits; $1.00→$0.30 /1K | VERIFIED | https://serper.dev/ |
| V-6 | Common Crawl CDX index served at index.commoncrawl.org (pywb); collinfo.json lists indices; no auth | VERIFIED | https://index.commoncrawl.org/ |
| V-7 | OpenAlex returns works/authors/sources/institutions/topics/publishers/funders; CC0; free API key | VERIFIED | https://developers.openalex.org/ |
| V-8 | SEC EDGAR REST APIs (Submissions/Company Facts/Concept/Frames) on data.sec.gov; no auth/API key | VERIFIED | https://www.sec.gov/search-filings/edgar-application-programming-interfaces |
| V-9 | GLEIF API returns LEI + ownership/hierarchy data from the Golden Copy; public | VERIFIED | https://www.gleif.org/en/lei-data/gleif-api |
| V-10 | OpenRouter is OpenAI-compatible; Bearer auth | VERIFIED | https://openrouter.ai/docs/api-reference/overview |
| V-11 | OpenRouter free models via `:free` suffix; 20 req/min, 50/day (<$10) → 1,000/day (≥$10) | VERIFIED | https://openrouter.zendesk.com/hc/en-us/articles/39501163636379-OpenRouter-Rate-Limits-What-You-Need-to-Know |
| U-1 | Competitor capability descriptions (ZoomInfo/Clay/Apollo/6sense/Cognism/Common Room) | UNVERIFIED | product knowledge; RI-5 |
| U-2 | SEC EDGAR exact rate cap (~10 req/s) + `User-Agent` requirement specifics | UNVERIFIED | SEC fair-access policy; not confirmed by V-8 fetch |
| U-3 | GLEIF / OpenAlex "no key required" beyond stated key/allotment | UNVERIFIED | confirm in `07` /provider-audit |
| U-4 | OpenAlex historical "polite pool" via `mailto` | UNVERIFIED | superseded by API-key model; confirm in `07` |
| U-5 | All per-provider $/call, rate limits, coverage as applied to our volumes | UNVERIFIED | `07` + `11`; RI-5 |
| U-6 | Per-token LLM pricing / free-vs-paid share under load | UNVERIFIED | `11`; RI-2 |

## Open items

| ID | Item | Status | Owner |
|---|---|---|---|
| RF-OI-1 | Confirm SEC EDGAR `User-Agent` requirement + exact fair-access rate cap; encode in the adapter `CallPolicy` (`03`) | Open | Research + Backend |
| RF-OI-2 | Confirm GLEIF / OpenAlex auth-and-limit specifics against live 401/429 behavior at /provider-audit | Open | Research |
| RF-OI-3 | ADR-0009 human-policy confirmation for Serper/Tavily (DEPRIORITIZED) — RI-OI-1 in `00` | Pending | Security + Product |
| RF-OI-4 | Per-token LLM pricing table for the cost model (`11`) | Deferred to `11` | Backend + ML |
| RF-OI-5 | Re-fetch vendor pricing before GA (prices drift) and refresh V-2/V-4/V-5 | Open | Research |
