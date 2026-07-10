# 11 — Cost Model

> **Status:** DRAFT · **Owner:** Staff ML Engineer + Principal Backend Engineer · **Last updated:** 2026-07-09 · **Gated by:** /architecture-review, /provider-audit, /scale-check

> This document is the **cost-efficiency design** for the Research & Intelligence platform: the
> **free-first LLM economics** (OpenRouter free pool primary, paid escalation gated), the **G4 token
> lifecycle** (estimate → reserve → charge-actual → reconcile with an over-reserve buffer), the
> **search/dataset pricing** that feeds a worked **cost-per-Dossier** estimate, the **caching** levers
> that make a re-Run nearly free, and **per-Tenant AI budgets** via `configver`. It realizes **ADR-0026**
> (LLM-as-egress-adapter + deterministic free→paid cascade) and consumes **ADR-0025** (search/dataset
> providers), **ADR-0028** (Dossier + freshness TTL), and **ADR-0029** (embeddings deferred — no
> per-token embedding cost). It **extends — never re-litigates** — ADR-0007 (Pandora reservation-value
> cascade), ADR-0011 (Postgres ledgers), and the G4 doctrine (**budgets alert, Cost Ceilings enforce**).
> The governing invariant is verbatim: **"the model proposes, a deterministic gate disposes"** — the
> free→paid escalation is disposed by a deterministic gate, **never** by an LLM. Terms follow the
> Glossary (`docs/00-Project-Overview.md §7` + [`00 §6`](00-overview.md)): Tenant, Company, Provider,
> Dossier, Research Run, Agent Task, Model Cascade, Source Type. **Pricing cited from vendor/analyst
> pages is tagged VERIFIED with a `source_url` + `verified_date`; every derived cost estimate and
> token count is a design target tagged UNVERIFIED** (`00 §8`, RI-2/RI-5).

---

## 1. The free-first thesis

The product requirement is to run **primarily on free / low-cost models**, escalating to paid only when
a deterministic gate demands it, under the G4 cost ceiling (ADR-0026 §Context). The cost story has four
compounding levers, each an existing platform mechanism:

1. **Free-first LLM carrier** — the OpenRouter free-model pool is the default cascade head; paid tiers
   are **gated escalations only** (§2, §3).
2. **Free authoritative datasets** — SEC EDGAR, OpenAlex, GLEIF, and Common Crawl (index) carry no
   per-call vendor fee; only search discovery has a metered cost (§4).
3. **Caching** — the G2 idempotency ledger *is* the research cache; a re-Run of an unchanged subject
   pays **zero** LLM tokens and zero provider calls (§5).
4. **Zero new infrastructure cost** — no LLM/vector SDK, no Redis, no `pgvector`, no embedding tokens
   (ADR-0016/0026/0029); the AI layer reuses the egress tier and Postgres it already pays for.

## 2. LLM economics — free-first pool, gated paid escalation

Per Agent Task, candidate models are ordered **free → mid → paid** by the ADR-0007 reservation-value
cascade; the **deterministic gate disposes** the accept/escalate/stop decision on deterministic signals
only — schema-valid output, G4 budget, attempt count, cross-model agreement — **never** an LLM's
self-reported confidence (`04 §5`, ADR-0026). Free models therefore carry the default load and paid
models are a gated escalation, keeping **paid-token share below the configured cap** (UNVERIFIED, RI-2).

| Tier | Example model (adapter) | Input $/M tok | Output $/M tok | Role |
|---|---|---|---|---|
| **free (head)** | `openrouter` `:free` pool — DeepSeek R1, Llama 3.3 70B, Qwen3-Coder (rotating lineup) | **$0.00** | **$0.00** | Default carrier for every task |
| **mid (gated)** | `openai` GPT-4o mini | **$0.75** | **$4.50** | Escalation on schema-invalid / disagreement |
| **mid (gated)** | `anthropic` Claude Haiku 4.5 | **$1.00** | **$5.00** | Escalation; prompt-caching cuts input up to ~90% for stable system prompts |
| **paid (gated)** | larger paid models via `openrouter-paid`/`openai`/`anthropic` | vendor passthrough | vendor passthrough | Last-resort escalation, budget-permitting |

> **VERIFIED** — OpenRouter `:free` models are **$0/M input, $0/M output** with a 200K context window,
> usable at a $0 balance; the free tier allows **50 requests/day + 20 requests/minute**, and a one-time
> $10 credit purchase raises the daily `:free` cap to **1,000 requests/day** (the 20 rpm cap is
> unchanged). **Failed attempts still count** toward the daily quota, and the free-model lineup
> **rotates** (a model free today may not be tomorrow). `source_url`: https://openrouter.ai/pricing ,
> https://openrouter.ai/openrouter/free · `verified_date`: 2026-07-09.

> **VERIFIED** — OpenAI **GPT-4o mini ≈ $0.75/M input, $4.50/M output**; Anthropic **Claude Haiku 4.5 ≈
> $1.00/M input, $5.00/M output**, with prompt caching reducing effective input cost up to ~90% for
> stable prompts (2026 rates). `source_url`:
> https://intuitionlabs.ai/articles/ai-api-pricing-comparison-grok-gemini-openai-claude ,
> https://www.finout.io/blog/openai-vs-anthropic-api-pricing-comparison · `verified_date`: 2026-07-09.

**Operational consequence of the free-tier limits.** The 50-req/day + 20-rpm free cap (and the 1,000/day
lifted cap) is **per OpenRouter key**, so the Key-Pool machinery is load-bearing: free-model throughput
scales by holding a **pool of free-tier keys** selected O(1) by the existing rotation engine
(`docs/waterfall-dashboard/02 §2.5`), with breaker-on-`QUOTA` sending a Run to the next key or the mid
tier. Because *failed attempts count*, the G3 `MaxAttempts:1` transport bound and the struct-validation
re-ask cap (`04 §6`) directly protect the free quota. Exact per-key throughput is **UNVERIFIED** (RI-5).

## 3. Token lifecycle — estimate → reserve → charge-actual → reconcile (G4)

LLM cost is nondeterministic per call (token counts vary), so G4 uses **reserve-on-estimate /
charge-on-actual** with an **over-reserve buffer + reconcile** — the ADR-0026 §Decision contract:

```mermaid
flowchart LR
    est["1 ESTIMATE\ntokens ≈ f(prompt_version, input size, task)\n× over-reserve buffer"]
    res["2 RESERVE\ndebit estimated cost from the\nper-Tenant AI budget (G4) BEFORE the call"]
    call["LLM provider.Call\n(CallPolicy{60–90s, MaxAttempts:1})"]
    chg["3 CHARGE ACTUAL\nprompt_tokens + completion_tokens\n→ usage_events (0015 cols) · llm_cost_usd"]
    rec["4 RECONCILE\nnightly: release (reserved − actual)\nback to the budget"]
    est --> res --> call --> chg --> rec
```

| Step | Mechanism | Store |
|---|---|---|
| **1 Estimate** | A token estimator (prompt-template size + input hash + task type) produces an expected prompt+completion token count; multiplied by an **over-reserve buffer %** so a larger-than-expected response cannot overshoot the ceiling. Estimator + buffer % is an Open item (RI-OI-2 / COST-RI-1). | code (`internal/ai`) |
| **2 Reserve** | The **estimated** cost is debited from the per-Tenant AI budget **before** the call — a Run that cannot reserve fails fast `QUOTA` (`06 §6`). The aggregate Dossier ceiling is reserved once before collection (ADR-0028). | G4 ledger / `configver/budget` |
| **3 Charge actual** | On success, **actual** `prompt_tokens` + `completion_tokens` and `llm_cost_usd` are written to the new **`usage_events` token/model columns (migration 0015)** — non-LLM rows leave them NULL — so per-model spend and free-vs-paid share are queryable alongside every other provider call. **No new ledger table** (one-owner-per-table). | `usage_events` (0015) |
| **4 Reconcile** | A nightly job releases `(reserved − actual)` back to the budget (the same reconcile discipline as the `key_budgets` `day_leased` sweep). Free-model calls charge **$0**, so the buffer is released in full. | reconcile job |

Because the **free tier charges $0**, the reserve on a free-path task is a small fixed buffer that
reconciles to zero — free Runs consume budget only when they *escalate*, which is exactly the gate's
intent.

## 4. Search & dataset pricing

Discovery search is metered; the authoritative datasets the Dossier leans on are **free**. DEPRIORITIZED
crawl-provenance search (Serper/Tavily) is off by default and compliance-gated (`03 §2`, ADR-0009), so
it does not contribute to the default cost path.

| Provider | Category | Status | Price | Tag |
|---|---|---|---|---|
| **Brave Search** | `search` | ACTIVE-CANDIDATE | Search/Data plan (Web Search incl. the AI **LLM Context** endpoint): **$5 per 1,000 calls/mo**; Answers (AI) plan: **$4 / 1,000 queries + $5/M input + $5/M output tokens**; **$5 monthly credits**; **2 rps** default. Free tier **removed Feb 2026** (metered billing). | VERIFIED |
| **SEC EDGAR** | `dataset` | ACTIVE-CANDIDATE | **Free**, no API key; **≤10 requests/second per IP**; no daily limit. | VERIFIED |
| **OpenAlex** | `dataset` | ACTIVE-CANDIDATE | Freemium: **free** (≈$0.10/day usage without a key; ≈$1/day with a free key), **≤10 req/s**, up to **100k calls/day**; API keys required from Feb 13. | VERIFIED |
| **GLEIF** (LEI) | `dataset` | ACTIVE-CANDIDATE | **Free**, public, no auth; **no usage fees or rate limits**. | VERIFIED |
| **Common Crawl** (CDX index) | `dataset` | ACTIVE-CANDIDATE (index-only) | Open dataset; no per-call vendor fee for the index API (egress/compute only). WARC bodies deferred (`03 §3`). | UNVERIFIED (open data; not price-fetched) |
| **Serper** | `search` | **DEPRIORITIZED** | Per-1,000-query pricing; off by default, compliance-gated. | UNVERIFIED |
| **Tavily** | `search` | **DEPRIORITIZED** | Free **1,000 credits/mo**; PAYGO **$0.008/credit**; subscriptions **$0.0075 → $0.005/credit** (Researcher $30/mo, Professional $100/mo). | VERIFIED |

> **VERIFIED** — Brave Search API: Data/Search plan (incl. the LLM-Context endpoint) **$5 per 1,000
> calls/month**; Answers plan **$4/1,000 queries + $5/M input + $5/M output tokens**; **$5** monthly
> credits; 2 rps default; the free tier was **removed in February 2026** in favor of metered billing.
> `source_url`: https://costbench.com/software/ai-search-apis/brave-search-api/ ,
> https://brave.com/learn/best-search-api-2026/ ,
> https://www.implicator.ai/brave-drops-free-search-api-tier-puts-all-developers-on-metered-billing/ ·
> `verified_date`: 2026-07-09.

> **VERIFIED** — SEC EDGAR API is **free, no key, ≤10 req/s per IP, no daily limit**
> (`source_url`: https://www.sec.gov/search-filings/edgar-search-assistance/accessing-edgar-data );
> OpenAlex is **freemium** (free daily usage, API key from Feb 13, ≤10 req/s, ≤100k calls/day)
> (`source_url`: https://developers.openalex.org/guides/authentication ,
> https://help.openalex.org/hc/en-us/articles/24397762024087-Pricing ); GLEIF is **free, public, no
> rate limits** (`source_url`: https://www.gleif.org/en/lei-data/gleif-api ). `verified_date`:
> 2026-07-09.

> **VERIFIED** — Tavily: free **1,000 credits/mo**, PAYGO **$0.008/credit**, subscriptions
> **$0.0075→$0.005/credit** (Researcher $30/mo, Professional $100/mo). `source_url`:
> https://www.tavily.com/pricing , https://docs.tavily.com/documentation/api-credits · `verified_date`:
> 2026-07-09.

The **G4 routing consequence** (ADR-0009 gate): free/clean datasets are routed **first**, metered Brave
search **second**, and DEPRIORITIZED search **last / off** — the reservation-value cascade spends the
cheapest adequate source first (`03 §8`).

## 5. Caching as cost control

The single biggest cost lever is **not paying twice** — the G2 idempotency ledger doubles as the
research cache, with **no Redis and no new store** (ADR-0029):

| Lever | Mechanism | Saving |
|---|---|---|
| **Cache-on-first-success (LLM)** | The G2 key `hash(tenant, subject, task_type, model_slug, prompt_version, input_hash, config_version)` caches the first valid answer; a re-Run with unchanged inputs pays **zero LLM tokens** (`04 §8`, ADR-0026). | 100% of repeat LLM cost |
| **Cache-on-first-success (collection)** | Search/dataset calls are keyed on `config_version` + normalized subject + slug; a replay returns the stored result (`03 §6`). | 100% of repeat provider cost |
| **Dossier freshness-TTL reuse** | `GET /v1/dossiers/{domain}` serves the **latest stored** Dossier; background refresh re-enqueues only on a per-section TTL (long for filings/LEI, short for news/discovery) rather than recomputing on every read (`03 §6`, ADR-0028). | avoids full re-Runs |
| **Intent coalescing** | `intent_refresh` keyed on `company_domain` collapses concurrent triggers into one compute (`05 §5`). | avoids duplicate scoring |
| **`wanted_sections` pruning** | Skips Agent Tasks for unrequested sections → fewer tokens/calls per Run (API-OI-2). | proportional to pruned sections |
| **No embeddings** | Dedup/retrieval uses deterministic keys + Postgres full-text; **zero embedding tokens** (ADR-0029). | 100% of embedding cost |

A prompt-template edit mints a **new** `prompt_version` → a new cache key (no stale reuse); this is the
correctness cost of caching, and it is bounded (only changed prompts re-pay).

## 6. Per-Tenant AI budgets via `configver`

Per-Tenant AI/research spend is governed by a **budget**, honoring the G4 doctrine **budgets alert, Cost
Ceilings enforce**:

- **Ceilings enforce** — the aggregate Dossier ceiling (reserved before collection) and the per-step
  token reservation (§3) are the **hard** G4 enforcement; a Run that cannot reserve fails `QUOTA`.
- **Budgets alert** — a per-Tenant AI budget row (`budgets`, scope `tenant`/`workflow`) with `alert_pct`
  thresholds emits alerts as spend approaches the cap; a misconfigured budget can never corrupt a Run
  (`docs/waterfall-dashboard/02 §2.3`, alerts observe/notify, never gate).
- **Config, not code** — LLM routing/tiers (`llm_route`) and intent weights (`intent_weights`) are
  `config_versions` kinds via `configver` (`04 §7`, `05 §4`); per-tier budget caps live in the
  `llm_route` payload, versioned and approval-gated. Editing them mints a new version pinned into the
  G2 key (no stale spend policy).

## 7. Worked cost-per-Dossier estimate (UNVERIFIED)

All token counts, call counts, and totals below are **design targets tagged UNVERIFIED** (RI-2/RI-5),
using the §2/§4 **VERIFIED** unit prices. The point is the *shape*: a free-path Dossier is dominated by
a few metered search calls (cents), and a paid escalation adds a small increment.

Assumed per full Dossier [UNVERIFIED]: ~10 Agent Tasks; ~50k input + ~15k output LLM tokens total;
~3 Brave search calls; free datasets (EDGAR/OpenAlex/GLEIF) as needed.

| Cost component | Free path | Escalated path (≈20% of tokens to GPT-4o mini) | Basis |
|---|---|---|---|
| LLM input tokens | $0.00 (free pool) | ~10k × $0.75/M ≈ **$0.0075** | §2 VERIFIED unit price |
| LLM output tokens | $0.00 (free pool) | ~3k × $4.50/M ≈ **$0.0135** | §2 VERIFIED unit price |
| Search (Brave) | 3 × ($5/1,000) ≈ **$0.015** | 3 × ($5/1,000) ≈ **$0.015** | §4 VERIFIED unit price |
| Datasets (EDGAR/OpenAlex/GLEIF) | **$0.00** (free) | **$0.00** (free) | §4 VERIFIED |
| Embeddings / vector store | **$0.00** (deferred) | **$0.00** | ADR-0029 |
| **Est. total / Dossier** | **≈ $0.015** | **≈ $0.036** | UNVERIFIED (RI-2) |

Two honest caveats: (a) **failed free attempts still consume the free-key quota** even at $0 dollar cost,
so the *real* free-tier constraint is requests/day, not dollars (§2) — sizing the free-key pool is the
actual scaling lever (RI-5, `10 §3`); (b) the escalated column assumes the gate escalates only a minority
of tokens — the paid-token **share** cap is the controlled variable, measured in telemetry (§8, RI-2).

## 8. Cost observability

Cost is measured, not asserted (`13`): the `usage_events` token/model columns (0015) feed the metric
families **`llm_tokens_total`**, **`llm_cost_usd`**, and **`search_calls_total`**; a dashboard panel
shows **free-vs-paid token share** per Tenant and per model, and the paid-share cap (RI-2) is a release
obligation converted from UNVERIFIED to measured over a real run (ADR-0026 §Verification, `13`). Per-model
breaker state and Key-Pool exhaustion surface the free-tier quota pressure of §2.

## Open items

| ID | Item | Status | Owner |
|----|------|--------|-------|
| COST-RI-1 | Token estimator + over-reserve buffer % (feeds G4 reserve, §3; = RI-OI-2 / AI-OI-1) | Draft | Backend + ML |
| COST-RI-2 | Free-OpenRouter-key **pool size** to sustain target Run throughput given 50/1,000-req-day caps | UNVERIFIED until `10` LT gates | Backend + SRE |
| COST-RI-3 | Default `llm_route` tiers + per-tier budget caps + paid-token-share cap value (RI-2) | Draft (§2, §6) | ML + Product |
| COST-RI-4 | Measured per-Dossier token/call counts to replace §7 UNVERIFIED estimates | UNVERIFIED until cost telemetry over a real run (`13`) | ML + Backend |
| COST-RI-5 | Serper / Common Crawl exact pricing citations (currently UNVERIFIED) | Pending `01`/`07` | Research |
| COST-RI-6 | Per-Tenant AI budget defaults + `alert_pct` thresholds | Draft (§6) | Product + Backend |
