# 00 — Overview: Enterprise Research & Intelligence Platform

> **Status:** DRAFT · **Owner:** Principal Enterprise Software Architect · **Last updated:** 2026-07-09 · **Gated by:** /architecture-review, /security-audit, /scale-check, /provider-audit

> This document is the anchor for the `docs/research-intelligence/` series. It **extends** the
> Waterfall Enrichment Engine (`docs/00`–`42`, ADRs 0000–0024) into a full **Research, Intelligence,
> Intent, and AI-automation platform**, and it **supersedes the ingest-only framing** of
> [`docs/14-Intent-Engine.md`](../14-Intent-Engine.md) (which becomes the *third-party ingest* lane;
> the *computed* intent engine is defined in [`05-intent-methodology.md`](05-intent-methodology.md)).
> The Glossary in [`docs/00-Project-Overview.md §7`](../00-Project-Overview.md) is mandatory and
> canonical; §6 below adds series-specific terms in the same style. **Nothing here forks a locked
> decision** — every new subsystem slots into the ratified architecture (ADR-0010 modulith+dataplane,
> ADR-0011 Postgres-RLS, ADR-0012 API strategy, ADR-0013 Kafka, ADR-0014 Temporal-cost-gated,
> ADR-0015 cloud topology) and honors all five correctness gates.

---

## 1 Vision

The platform today is an **API-first, headless enrichment engine**: given a Record and wanted Fields,
it plans a Waterfall, executes bounded Provider calls, and records Provenance under five gates. This
series grows it into an **Enterprise Research & Intelligence Platform** in the class of
ZoomInfo / Clay / Apollo / 6sense / Cognism — but significantly more cost-efficient, because it
reuses the enrichment engine, the ~145-adapter Provider machinery, the durable queue/worker fabric,
the RLS multi-tenancy, and the full admin dashboard, and it runs its AI layer on **free-first models
through the existing egress tier** rather than a new dependency stack.

The headline capability is **domain → Dossier**: one API call takes a Company domain (or name /
LinkedIn / email / phone) and returns a normalized, CRM-ready **Dossier** — firmographics,
technographics, contacts, hiring and buying signals, computed multi-class **intent**, news,
competitors, an AI summary, per-section confidence, and full source provenance — assembled with **no
manual intervention**.

Five product verbs, each with a concrete mechanism, all under the governing invariant **"the model
proposes, a deterministic gate disposes"**:

1. **Collect** — search APIs + public bulk datasets register as ordinary Providers through the SSRF-
   guarded egress; browser automation and DOM scraping stay permanently banned (ADR-0025).
2. **Reason** — LLMs are egress adapters (ADR-0026); specialized agents (company/news/technology/
   competitor/hiring/market/SEO/summarization/JSON-validation) run as a deterministic DAG, free-first
   with a gate-disposed cost cascade — the model never picks a spend or a tool.
3. **Score** — computed intent across ten classes by an auditable signal→decay→fuse→calibrate pipeline
   with per-signal reasoning (ADR-0027); LLM outputs enter only as *proposed raw signals*.
4. **Deliver** — a domain→Dossier Research API (async 202 + sync preview + HMAC webhook), a normalized
   `crm_ready` projection, and roadmap CRM push through the single egress-proxy (ADR-0028/0030).
5. **Govern** — every new external/LLM call passes G1–G5 + the SSRF egress; every AI-derived value is
   provenanced as `ai_inference` and never fused as a high-confidence fact; PII/DSAR + prompt-injection
   content-trust are a shared baseline (`09`).

Design scale targets — 100+ concurrent research jobs per user, thousands of concurrent jobs across
tenants, millions of research requests — are engineering assumptions carried as **UNVERIFIED** until
load-tested (§8).

The Go backend stays **stdlib-only** (ADR-0016/0022): the LLM/search/dataset layer is plain HTTP+JSON
through the existing `internal/provider` machinery — **zero new Go dependency**. Embeddings/RAG are
**deferred** (ADR-0029) to protect that discipline and the free-first budget.

## 2 Scope — the subsystems

### 2.1 Core spine (fully designed in this series)

- **AI Research Engine + orchestration** — `internal/ai` (typed agent tasks: `company_research, news,
  intent, technology, seo, competitor, hiring, market, summarization, json_validation`) +
  `internal/research/orchestrator` (deterministic DAG fan-out on the existing `internal/job` +
  `internal/durable` lane — **not** Temporal by default, honoring ADR-0014). Design: [`04`](04-ai-pipeline.md).
- **Data Collection Engine** — new Provider categories `search` / `dataset` / `news` as registry
  adapters through the egress tier (ADR-0025). Design: [`03`](03-data-collection.md), [`07`](07-provider-expansion.md).
- **Computed Intent Engine** — `internal/intent/{signal,score}`: ten-class signal→decay→fuse→calibrate
  with reasoning, weights as versioned config, async-only (ADR-0027). Design: [`05`](05-intent-methodology.md).
- **Research API + Dossier** — `POST /v1/research`, `GET /v1/research/{id}`, `GET /v1/dossiers/{domain}`;
  the Dossier JSON + CRM-ready schema (ADR-0028). Design: [`06`](06-research-api-schema.md).

### 2.2 Roadmap (specified, not core-designed) — [`15`](15-roadmap.md)

News & market monitoring depth, hiring-signal analysis, company-intelligence gaps (competitors,
acquisitions, funding rounds, partnerships, locations as Dossier objects), CRM outbound connectors
(ADR-0030), agent-orchestration depth, and the embeddings/RAG revisit trigger (ADR-0029).

### 2.3 Module → service / owned tables / endpoint map

Table ownership follows the one-owner-per-table registry (extends `docs/waterfall-dashboard/03`).
Config-versioned data (prompts, LLM routing, intent weights) reuses `internal/dash/configver` and
adds **no new table**. Provider adapters are code; the catalog is their projection (ADR-0023). All
`/v1/admin/*` endpoints keep the dashboard conventions (cursor pagination, `Idempotency-Key` on
writes, uniform error body); the research/enrichment surface stays on the `enrichapi` deployable.

| Subsystem | Backing package(s) | Plane | Owns tables (migration) | Endpoint group |
|---|---|---|---|---|
| AI agent library | `internal/ai` | control-plane | — (none) | — (internal) |
| AI research orchestration | `internal/research` | control-plane (runs on execution-engine workers) | `research_runs`, `research_steps`, `research_dossiers`, `research_sources` (**0015**); + token/model cols on `usage_events` | `POST /v1/research`, `GET /v1/research/{id}`, `GET /v1/dossiers/{domain}` |
| Data collection | `internal/provider/adapters/*` (categories `search`/`dataset`/`news`/`llm`) | data-plane egress | — (catalog rows only, registry projection) | surfaced via existing `/v1/admin/providers*` |
| Computed intent | `internal/intent/{signal,score}` | control-plane | `intent_signals` (partitioned), `intent_scores` (**0016**); weights = `config_versions` kind `intent_weights` | `POST /v1/intent/refresh`, `GET /v1/intent/accounts/{domain}` |
| AI routing / prompts (admin) | `internal/dash/airouting` (thin service over `configver`) | control-plane | — (`config_versions` kinds `ai_prompt`, `llm_route`) | `/v1/admin/ai/prompts`, `/v1/admin/ai/models` |
| Research monitoring (admin) | `internal/dash/research` | control-plane | — (reads `research_*`) | `/v1/admin/research/runs` |
| Intent surface (admin) | `internal/dash/intent` | control-plane | — (reads `intent_*`, owns `intent_weights` config) | `/v1/admin/intent/weights` |
| News/market *(roadmap)* | `internal/news` | control-plane + data-plane egress | `news_items`, `market_signals` (**0017**) | — |
| CRM outbound *(roadmap)* | `internal/crm` (push via the **single** egress-proxy) | control-plane + egress direction | `crm_connections`, `crm_field_maps`, `crm_push_ledger` (**0018**) | `/v1/admin/crm/connections` |

**Global migration ledger (strictly sequential; no duplicates):** **0015** research core + `usage_events`
token columns · **0016** intent signals/scores · **0017** *(roadmap)* news/market · **0018** *(roadmap)*
CRM. Config kinds `ai_prompt`, `llm_route`, `intent_weights` reuse migration 0006 — **no new table**.
Field-vocabulary additions (6 scalars, §6) are code+doc only, **not** a schema change (ADR-0023).

### 2.4 Dashboard extensions — [`08`](08-dashboard-extensions.md)

New admin modules `internal/dash/airouting` → `web/features/aimodels`; `internal/dash/research` →
`web/features/airesearch`; `internal/dash/intent` → `web/features/intent`. Data-collection Providers
surface automatically through the existing `internal/dash/providers` + `web/features/providers`.
Reuse SSE (ADR-0019), telemetry, cost, health, and approvals; enforce the **no-orphan-UI** rule (every
panel binds to a real service/table/endpoint).

## 3 Non-goals

| # | Non-goal | Rationale / pointer |
|---|----------|---------------------|
| 1 | **No scraping / browser automation, ever.** Search APIs return URLs for *discovery only*; a returned URL is resolved only via another registered Provider API, never fetched-and-DOM-parsed. Common Crawl is **index-only** (WARC-body extraction deferred). | ADR-0025 (supersedes 0002) |
| 2 | **No new Go dependency.** LLM/search/dataset are egress adapters (HTTP+JSON); JSON validation is struct-based; no LLM/vector SDK, no Redis client, no `pgvector`. | ADR-0016/0022/0026/0029 |
| 3 | **No model-driven spend or tool execution.** The free→paid cascade and every tool/step is disposed by a deterministic gate over deterministic signals (schema-valid, budget, attempt count, agreement) — never LLM self-confidence, never a model-chosen tool call. | ADR-0026; governing invariant |
| 4 | **No second internet route.** CRM push (roadmap) is an outbound *direction* of the existing egress-proxy, not a new deployable; the egress-proxy stays the sole SSRF boundary + key custodian. | ADR-0010/0030 |
| 5 | **Not a new architecture.** Modulith control-plane + stateless data-plane, Postgres-RLS, Kafka, Temporal-cost-gated stand; new subsystems extend them, they do not re-litigate them. | ADR-0010/0011/0013/0014 |
| 6 | **Intent is async-only.** Computed intent never runs on the sync per-Field enrichment path; a sync Dossier preview shows last-known intent or `pending`. | ADR-0027 |
| 7 | **AI values are never facts.** Every AI-derived value is provenanced `source_type=ai_inference`, kept distinct, and never fused as a high-confidence sourced fact. | ADR-0026/0028 |

## 4 Audiences & RBAC recap

The fixed three-role matrix (ADR-0018/0020) is unchanged; all enforcement is server-side. Research and
intent are Tenant-scoped like enrichment. Prompt templates + LLM routing default to the sentinel
`platform` Tenant (operator-owned), with optional per-Tenant override; publishing a prompt/route/weight
version is approval-gated exactly like routing/workflow config (four-eyes for blast-radius verbs).
`operator` manages the AI model catalog, platform prompts, and cross-tenant health/cost (enumerated,
audited reads only); `tenant_admin` governs their Tenant's research/intent config, budgets, and CRM
connections; `tenant_user` reads their Tenant's Dossiers and intent.

## 5 Locked decisions summary

| # | Decision | Summary | ADR |
|---|----------|---------|-----|
| 1 | Data collection | Search APIs + public bulk-dataset APIs are legitimate server-side Providers; browser/DOM scraping permanently banned; returned-URL resolves only via another Provider API; Common Crawl index-only. Crawl-provenance search (Serper/Tavily) → DEPRIORITIZED via the ADR-0009 gate. | **0025** (supersedes 0002, refines 0009) |
| 2 | LLM-as-egress-adapter | LLMs = HTTP+JSON egress adapters (category `llm`); G2–G5 apply; deterministic free→paid cost cascade; routing/prompts = `config_versions` kinds; zero new Go dep; struct-based validation. | **0026** |
| 3 | Computed intent | Ten-class signal→decay→fuse(log-odds)→calibrate(isotonic)→guardrailed score with per-signal reasoning; weights versioned; async-only; LLM outputs are proposed raw signals only. | **0027** |
| 4 | Research-dossier API + Fields | Dossier = research-owned composite JSON referencing canonical Fields; six new single-valued Fields (33→39) DOC-FIRST; all multi-valued/relational data stays Dossier-only. | **0028** |
| 5 | Embeddings/RAG | Deferred; no vector client/SDK/`pgvector` now; Postgres-native path + revisit trigger recorded. | **0029** |
| 6 | CRM outbound | Push through the single egress-proxy as a new outbound direction (not a new deployable); envelope-sealed CRM tokens injected at egress; idempotent pushes. Roadmap. | **0030** (preserves 0010) |

## 6 Glossary additions

Extends [`docs/00-Project-Overview.md §7`](../00-Project-Overview.md) — same rules: these names are
canonical for this series; existing terms (Tenant, Company, Person, Provider, Waterfall, Field,
Confidence, Provenance, Cost Ceiling, Idempotency Key, …) are used verbatim. The six new canonical
**Fields** (`twitter_url`, `facebook_url`, `github_url`, `crunchbase_url`, `company_ticker`,
`total_funding_usd`) are registered in `docs/00 §7`, not here.

| Term | Definition | Do **not** call it |
|------|------------|--------------------|
| **Dossier** | The research-owned composite intelligence document for a Company (firmographics, technographics, contacts, hiring/buying signals, intent, news, competitors, AI summary, confidence, provenance, `crm_ready`). Stored in `research_dossiers`; **not** a Field. | "profile" (that's `company_profile`, one section); "report" |
| **Research Run** | One execution of the research orchestrator for a subject, producing (or refreshing) a Dossier; recorded in `research_runs` with per-agent `research_steps`. | "job" loosely (an Enrichment Job is different); "research task" |
| **Agent Task** | A single typed unit of AI work (`TaskType`) with an input contract, a Prompt Version, and a typed output struct; runs as a `research_steps` row. | "prompt", "agent" (the library, not the unit) |
| **Prompt Version** | An immutable versioned prompt template (`config_versions` kind `ai_prompt`); its version is part of the G2 idempotency key. | "prompt" (that's the text); "template revision" loosely |
| **Model Cascade** | The deterministic free→mid→paid ordering of LLM models with a gate-disposed accept/escalate/stop decision (ADR-0026). | "fallback chain" loosely; "routing" (that's the config) |
| **Intent Signal** | A single normalized `{class, type, magnitude, observed_at, provider, confidence, cost}` observation feeding a class score; stored in `intent_signals`, losers retained. | "signal" bare; "event" |
| **Intent Class Score** | A per-class computed score (`intent_scores`) with confidence and a `reasoning` breakdown; distinct from the single-valued `intent_score` Field written back to the waterfall. | "intent score" (ambiguous with the Field) |
| **Search / Dataset / LLM Provider** | Provider categories added by this series (`search`, `dataset`, `llm`, roadmap `news`) — server-side APIs reached only through the egress tier. | "scraper", "crawler" (banned); "tool" loosely |
| **Source Type** | The provenance origin of a value: `api` \| `dataset` \| `ai_inference`. AI-inferred values are never fused as high-confidence facts. | "source" bare |

## 7 Document map

The `docs/research-intelligence/` series comprises 17 documents + the research OpenAPI. Each carries
the header/footer discipline, inline Mermaid, glossary-verbatim terms, UNVERIFIED tags on unmeasured
claims, and a closing Open-items table.

| Doc | Title | One-line scope |
|-----|-------|----------------|
| 00 | Overview | This document — vision, scope, non-goals, RBAC recap, locked decisions, glossary, document map, UNVERIFIED register, Self-Verification Record. |
| 01 | Research findings | Cited competitor + provider/API landscape (Brave/Tavily/Serper/Common Crawl/OpenAlex/EDGAR/GLEIF/OpenRouter); adopted/rejected patterns; verification register. |
| 02 | Architecture | C4 container view of the extended platform; where each subsystem sits in the modulith/data-plane; hard-gate compliance table; sequence + deployment diagrams. |
| 03 | Data collection | Search/dataset/news adapters; the ADR-0025 no-scraping boundary; egress allow-list expansion; SSRF rules; freshness/caching. |
| 04 | AI pipeline | LLM-as-egress-adapter; the agent task catalog; the deterministic orchestration DAG; model cascade + cost optimization; struct validation; prompt/route config. |
| 05 | Intent methodology | Ten-class signal taxonomy; decay→fuse→calibrate math; weights as config; reasoning/explainability; async lane; write-back ownership. |
| 06 | Research API + schema | `/v1/research` contract; the full Dossier JSON schema; sync-preview vs async; CRM-ready normalization; provenance rows; OpenAPI pointer. |
| 07 | Provider expansion | New categories/adapters; ADR-0009 inclusion status per provider; field-vocabulary extension (6 scalars); Dossier-only object catalog. |
| 08 | Dashboard extensions | New `internal/dash` modules + `web/features`; SSE/telemetry/cost/health/approvals reuse; no-orphan-UI. |
| 09 | Security, PII & DSAR | G1–G5 mapping for all new calls; content-trust baseline (untrusted fetched content, prompt-injection); PII/DSAR delete cascade; retention TTLs; egress-as-sole-route. |
| 10 | Scalability | 100+ concurrent research jobs/user via Kafka + execution-engine autoscale + durable-lane DAG fan-out (Temporal escalation-only, ADR-0014); Postgres/RLS concurrency reservations (no Redis); throughput model + load-test plan. |
| 11 | Cost model | Free-first LLM economics; token estimate/reserve/reconcile; search/dataset pricing (cited); per-tenant AI budgets via `configver`; caching. |
| 12 | Deployment | `enrichapi`/`dashboardd`/worker topology for the new subsystems; env/config reference; regional-cell fit (ADR-0015); rollout. |
| 13 | Observability | New metric families (`research_job_duration`, `llm_tokens_total`, `llm_cost_usd`, `intent_score_freshness`, `search_calls_total`); telemetry rollups; SSE; alert vocabulary. |
| 14 | Testing | Unit, integration (RLS cross-tenant zero-rows on every new table), contract parity, deterministic-cascade tests, chaos, security; UNVERIFIED→measured plan. |
| 15 | Roadmap | 1yr / 3yr / 5yr; news/hiring/CRM/company-intel/agent-depth/RAG; each with a trigger. |
| 16 | Implementation phases | Slices 21–27 (docs 43–49): scope, deliverables, executable acceptance criteria, G1–G5 proof tables, dependencies, deviation protocol. |
| — | `openapi-research.json` | Machine-readable research API contract (parity-tested against the handlers). |

New ADRs written alongside this series: **0025**–**0030** (§5). Existing ADRs 0001–0024 remain in force
and are cited by number throughout; **0002** is marked superseded by **0025**.

## 8 UNVERIFIED register

Every numeric scale, cost, or performance claim in this series is a **design target**, not a
measurement, and carries the UNVERIFIED tag until its named gate passes (`14`).

| ID | Claim | Design target | Tag | Verification gate |
|----|-------|---------------|-----|-------------------|
| RI-1 | Research throughput | 100+ concurrent research jobs/user; thousands across tenants | UNVERIFIED | Fleet load test (`14`, staging) |
| RI-2 | LLM cost | Free-model-first keeps paid-token share below the configured cap | UNVERIFIED | Cost telemetry over a real run (`11`) |
| RI-3 | Dossier latency | Sync preview p95 ≤ ~3s; async full Dossier within SLA | UNVERIFIED | Latency test (`10`) |
| RI-4 | Intent accuracy | Calibrated class scores track observed conversion | UNVERIFIED | Backtest against labels (`05`) |
| RI-5 | Provider pricing/limits | Per-provider $/call, rate limits, coverage | UNVERIFIED | Cited from vendor docs (`01`, `07`, `11`) |
| RI-6 | Search freshness | Search/dataset recency adequate for signals | UNVERIFIED | Freshness measurement (`03`) |

## 9 Self-Verification Record (design-time)

Each hard invariant, and how this design satisfies it (proof obligations land as tests in the
implementation slices, `16`):

| Invariant | How the design satisfies it |
|-----------|------------------------------|
| **G1 tenant isolation** | Every new table (`research_*`, `intent_*`, roadmap `news_*`/`crm_*`) carries `tenant_id` + FORCE RLS (partitions too); hot-path role has no BYPASSRLS; `app.current_tenant`/`app.current_role` bound from the verified Principal. |
| **G2 idempotency** | Ledger-before-call on every search/dataset/LLM/CRM call; keys pin `config_version` + (for LLM) `model` + `prompt_version` + `input_hash`; Dossier + intent refresh are idempotent; LLM caching is cache-on-first-success. |
| **G3 bounded** | All new calls go through `provider.Call` with a `CallPolicy` (LLM/async `{Timeout:60–90s,MaxAttempts:1}`, ADR-0024) + breaker + capped retry; every fetch via the egress-proxy only. |
| **G4 cost ceiling** | Aggregate Dossier ceiling reserved before collection; per-step reserve/charge; LLM reserve-on-estimate/charge-on-actual tokens; per-Tenant AI budget via `configver`. |
| **G5 provenance** | Every Field + Dossier value records provider, `source_type`, cost, idempotency key, confidence; losers retained; `ai_inference` never fused as fact; queryable via `research_sources`. |
| **SSRF / single boundary** | Egress-proxy is the sole internet route in **and** out (CRM push is a direction of it, not a second egress); host allow-list extended per new adapter; a model cannot SSRF (no model-driven fetch). |
| **Stdlib-only** | No new Go dep: LLM/search/dataset = HTTP+JSON adapters; struct-based validation; no Redis client (Postgres/`job` reservations); no vector SDK/`pgvector` (RAG deferred). |
| **One-owner-per-table** | `internal/research` owns `research_*`; `internal/intent` owns `intent_*`; LLM routing/prompts/intent-weights are `configver` kinds; no shared ownership; canonical names fixed (no `dossiers` alias). |
| **Model proposes, gate disposes** | LLM/bandit only *propose*; deterministic gates dispose all spend/tool/escalation decisions over deterministic signals; intent scores are deterministic. |
| **Provider trichotomy** | Search/dataset/LLM classified by ADR-0009; Serper/Tavily DEPRIORITIZED pending human policy; EXCLUDED providers never registered. |
| **DOC-FIRST field vocab** | The 6 new scalars registered in `docs/00 §7` before any `field.go` change; multi-valued data stays Dossier-only. |

## Open items

| ID | Item | Status | Owner |
|----|------|--------|-------|
| RI-OI-1 | ADR-0009 human-policy confirmation for Serper/Tavily (DEPRIORITIZED) | Pending | Security + Product |
| RI-OI-2 | Token estimator + over-reserve buffer % (G4 on LLM) | Draft in `11` | Backend + ML |
| RI-OI-3 | Intent calibration-label sourcing (cold-start → offline-learning) | Draft in `05` | ML |
| RI-OI-4 | Common Crawl WARC-body extraction (deferred; needs its own ADR) | Deferred | Architecture |
| RI-OI-5 | All per-provider pricing/limits/coverage | UNVERIFIED until `01`/`07`/`11` | Research |
