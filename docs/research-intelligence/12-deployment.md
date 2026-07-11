# 12 — Deployment

> **Status:** DRAFT · **Owner:** GTM Infrastructure Engineer · **Last updated:** 2026-07-09 · **Gated by:** /architecture-review, /security-audit, /scale-check

> This document specifies how the Research & Intelligence subsystems are **deployed, configured, rolled out, and
> degraded** on the **existing** three deployables — **no new binary and no new infrastructure**. It realizes the
> deployment fit frozen in [`02-architecture.md §6`](02-architecture.md) and the placement in
> [`00-overview.md §2.3`](00-overview.md), and it inherits the dashboard's ratified deployment posture
> (`docs/waterfall-dashboard/11`: N-stateless-instances, blue-green vs canary, regional cell, degradation
> doctrine) and ADR-0010 (modulith + data-plane), ADR-0011 (Postgres-RLS), ADR-0013 (Kafka design-target),
> ADR-0014 (Temporal cost-gated), ADR-0015 (regional cell), ADR-0017 (envelope secrets), ADR-0024/0026 (LLM
> `CallPolicy`). The governing invariant is verbatim: **"the model proposes, a deterministic gate disposes"** —
> degradation touches read completeness/freshness only; the gates never degrade. Terms follow the Glossary
> (`docs/00-Project-Overview.md §7` + [`00 §6`](00-overview.md)). Every scale/latency/cost figure is a design
> target carried **UNVERIFIED** until the `10`/`13`/`14` gates measure it (`00 §8`).

---

## 1. Where each subsystem runs

The R&I subsystems slot into the three existing deployables (`cmd/enrichapi`, `cmd/enrichd`, `cmd/dashboardd`) and
the data-plane **egress-proxy**. No `cmd/*` is added; the diagram of `02 §2` is the topology.

| Subsystem | Package(s) | Deployable | Plane (ADR-0010) | Scaling axis | Release strategy (ADR-0015) |
|---|---|---|---|---|---|
| Research API handlers | `internal/api` (`/v1/research`, `/v1/research/{id}`, `/v1/dossiers/{domain}`, `/v1/intent/*`) | **`enrichapi`** | data-plane public API | request rate (stateless) | blue-green |
| Research orchestration DAG | `internal/research/orchestrator` | **`enrichd`** workers | control-plane logic on data-plane workers | job throughput (autoscale on queue depth) | canary + automated rollback |
| AI Agent library | `internal/ai` | in-process on **`enrichd`** | control-plane | with the worker fleet | canary (with `enrichd`) |
| Data-collection + LLM adapters | `internal/provider/adapters/*` (`search`/`dataset`/`news`/`llm`) | **egress-proxy** (within the data-plane) | data-plane egress | egress fan-out | **canary per new adapter** (§4) |
| Computed intent (async) | `internal/intent/{signal,score}` | **`enrichd`** (`job.Kind=intent_refresh`) | control-plane, async lane | job throughput | canary (with `enrichd`) |
| AI routing/prompts + research/intent admin | `internal/dash/{airouting,research,intent}` | **`dashboardd`** | control-plane admin | admin traffic (read-mostly) | blue-green |
| News/market *(roadmap)* | `internal/news` | `enrichd` | control-plane + egress | job throughput | canary |
| CRM outbound *(roadmap)* | `internal/crm` (push = a *direction* of the egress-proxy) | `dashboardd` (config) + egress-proxy (push) | control-plane + egress direction | admin + egress | behind its own gate (Slice 27+) |

Two placement facts drive the rest of this doc:

1. **The AI "agents" are not a deployable — they are config.** An Agent Task's behavior is its **Prompt Version**
   (`ai_prompt`) + the **Model Cascade** (`llm_route`); both are `config_versions` rows (ADR-0026). Rolling out or
   rolling back an agent is a **`configver` publish**, not a binary deploy (§4).
2. **Every new external call is an egress-proxy call.** Search/dataset/news/LLM providers — and roadmap CRM push —
   traverse the **single** SSRF-guarded egress-proxy; there is no second internet route and no model-driven fetch
   (ADR-0010/0025/0030).

## 2. Configuration & secrets reference

R&I configuration splits cleanly across three homes, and the split is load-bearing for security:

- **Provider API keys (LLM/search/dataset) are sealed key-pool rows, never env vars.** They are `provider_keys`
  under a `"<slug>:default"` key pool (`openrouter:default`, `brave-search:default`, …), envelope-sealed with the
  ADR-0017 backend (`DASH_MASTER_KEY` custody) and **injected at the egress boundary** by `provider.AuthInjector` —
  the adapter never holds a secret, exactly as every existing provider key (`03 §4`, ADR-0026 §Decision). **Zero new
  plaintext-key env variables.** Onboarding an LLM/search key = an operator import into its pool (`docs/waterfall-dashboard/11 §2` import path), reusing `DASH_MASTER_KEY`/`DASH_FINGERPRINT_PEPPER` already in the fleet.
- **AI budgets, cascade tiers, prompts, weights are versioned config in Postgres, not env.** Per-Tenant AI/intent
  budgets, the free→mid→paid tier order + per-tier caps (`llm_route`), prompt templates (`ai_prompt`), and intent
  weights/half-lives (`intent_weights`) are `config_versions` published via `configver` (approval-gated). They
  change with a publish + epoch bump, never a redeploy (`04 §7`, `05 §4`).
- **A small set of operational knobs are new env vars** on `enrichapi`/`enrichd`, read once at boot (Go duration
  syntax; fail-fast on malformed), consistent with the no-config-file discipline.

| Variable | Deployable | Required | Default | Description |
|---|---|---|---|---|
| `RESEARCH_MAX_CONCURRENT_RUNS_PER_TENANT` | enrichd | no | `100` **UNVERIFIED** | Per-Tenant concurrent Research Run cap (RI-1 design target; enforced by the queue reservation, `10`). |
| `RESEARCH_DAG_STEP_CONCURRENCY` | enrichd | no | `6` | Max concurrent Agent Tasks per Run (the six section tasks after `company_research`, `04 §4`). |
| `RESEARCH_SYNC_PREVIEW_MAX_CREDITS` | enrichapi | no | `<engine-default>` | G4 ceiling for the `?mode=sync` capped preview (firmographics + `company_profile` only; ADR-0028). |
| `LLM_CALL_TIMEOUT` | enrichd | no | `90s` | `CallPolicy.Timeout` for `llm`/async adapters (ADR-0024 shape `{60–90s, MaxAttempts:1}`); the transport bound, not the orchestrator re-ask loop. |
| `LLM_TOKEN_OVERRESERVE_PCT` | enrichd | no | `20` **UNVERIFIED** | G4 over-reserve buffer on the token estimate; reconciled to actual on success (OI in `11`). |
| `INTENT_REFRESH_SWEEP_INTERVAL` | enrichd | no | `1h` | Scheduled-sweep cadence for tracked accounts (`05 §5`); push/webhook triggers preferred over polling. |
| `INTENT_SCORE_FRESHNESS_TTL` | enrichd | no | `24h` **UNVERIFIED** | Age past which a class score is re-scored on read-trigger; feeds `intent.score_staleness_s` (`13 §4`). |

All secrets hygiene rules from `docs/waterfall-dashboard/11 §2` apply unchanged: KEK/pepper/DSNs come from the deploy
platform's secret store, never baked into images, never logged; `/metrics` and crash output never echo the
environment; the `secrets.Secret` wrapper redacts. **No provider API key is ever an env var** — that would move
custody out of the single egress custodian (ADR-0010/0026).

## 3. Regional-cell fit (ADR-0015)

The regional cell — the unit of deploy, scale, blast radius, and data residency — is **unchanged**. Research and
intent are Tenant-scoped exactly like enrichment and live entirely inside the cell:

- **State stays in the cell's Postgres.** `research_*` (0015), `intent_*` (0016), and the `config_versions` kinds
  live in the cell's system of record under FORCE RLS; cells never share a database; cross-cell aggregation is out
  of scope for v1 (`docs/waterfall-dashboard/11 §1`).
- **Egress stays the cell's single boundary.** LLM/search/dataset calls (and roadmap CRM push) exit only through
  the cell's egress-proxy with its host allow-list + dial-time IP guard. Data residency is preserved because a
  Tenant's Runs, signals, and Dossiers never leave the cell (the LLM *provider* is external, but the request/response
  is bounded, logged-with-redaction, and never persisted outside the cell).
- **"Stamp another cell" still holds.** A new region is a new cell with its own key pools (its own sealed LLM/search
  keys), its own budgets, and its own egress allow-list — no global R&I service to coordinate.
- **Design-target stores remain interface seams.** The DAG fan-out uses the Postgres `internal/job` +
  `internal/durable` + `internal/pgoutbox` path now (ADR-0014: Temporal is cost-gated behind an interface, not the
  v1 fan-out); Redis/Kafka/ClickHouse are absent from the R&I deployment exactly as from the dashboard one
  (`docs/waterfall-dashboard/11 §1`, DS-5). Adopting one later is a per-store adapter swap, gated on a measured
  Postgres-comfort breach — **no R&I subsystem requires new infra** (ADR-0011/0013/0014).

## 4. Rollout & canary

Two rollout paths, chosen by *what* changed — code vs config:

| What changed | Path | Rollback | Why |
|---|---|---|---|
| **New/changed provider adapter** (`search`/`dataset`/`llm` `*.go` + registry row) | **Canary** on the deployable that carries egress (`enrichd`; `enrichapi` for the sync preview), per ADR-0015's "canary reserved for `enrichd` workers and new Provider adapters". The seeded `providers` row lands **`op_state='disabled'`** (DB default, `07 §3`) — the adapter ships dark and is enabled by an operator after review (DEPRIORITIZED sources need `compliance_review_status` first). | Disable the row (op-state) or roll back the canary; the host leaves the allow-list when the adapter is removed. | Adapters are code + a real vendor FQDN; a bad adapter is a code regression, so it gets automated-rollback canary, not a flip. |
| **New/changed AI agent behavior** (Prompt Version, cascade tiers, intent weights) | **`configver` publish** (draft → validate → **approval-gated** publish → epoch bump), *not* a deploy. Default to the sentinel `platform` Tenant with optional per-Tenant override; blast-radius verbs are four-eyes (`04 §7`, `05 §4`). | **Rollback = publish a prior version id** — instant, nothing destroyed; running Runs keep their pinned `config_version_id` (G5). | An agent is config, so its rollout inherits the versioned-config lifecycle: instant, audited, reversible at the pointer level — no binary in the loop. |
| **New migration** (0015 research, 0016 intent, 0017 R&I operator-read; roadmap 0018/0019) | Expand → migrate → contract, applied at boot by `pgmigrate.Apply` in strict filename order; each new table ships its RLS zero-rows test in the same slice (`14`, `16`). | Fix-forward (never a DDL rollback); binary N−1 must run against the expanded schema. | The dashboard's migration playbook (`docs/waterfall-dashboard/11 §6`) applies verbatim; migrations are strictly sequential (`00 §2.3`). |

**Config-epoch handoff during overlap** is safe by construction (shared DB state): both fleets observe the same
`ai_prompt`/`llm_route`/`intent_weights` epoch bump (NOTIFY + 1s poll) and rebuild resolvers identically; a Run in
flight keeps its pinned config. The one rule: a green fleet must not publish a `config_versions` payload-schema
field an overlapping blue validator rejects until blue is retired (payload-schema changes are expand→contract
subjects too, `docs/waterfall-dashboard/11 §6`).

## 5. Degradation modes

Ordered, reversible, always visible (Dossier `data_freshness` + per-section `confidence` + `provenance[]` make a
partial result explicit — degraded is never silent). Degradation touches **Dossier completeness/freshness only**;
G1–G5 never degrade — if a reserve or an RLS tx cannot proceed, the write **fails closed** with the uniform error
body rather than proceeding unbounded.

| Mode | Trigger (detected by) | Action | User-visible effect | Recovery |
|---|---|---|---|---|
| **LLM provider down** | per-model breaker opens (G3) after bounded failures on `provider.Call` | The Model Cascade **escalates one tier** to the next available model (free→mid→paid); if **all tiers** for a task are open, the orchestrator **skips that AI section** and marks it `pending`/degraded — it never blocks the Run or fabricates a value. | The affected Dossier section (`ai_summary`, `technographics`, …) is absent or `pending`, section `confidence` low; `provenance[]` shows the gap; other sections deliver. | Breaker half-opens and probes; a successful call restores the tier; a freshness-TTL re-Run backfills the skipped section (ADR-0026, `04 §5`). |
| **Search/dataset down** | breaker open / SSRF-guarded call error on a collection adapter | Collection proceeds with the **available** sources; the Dossier is assembled **partial**; DEPRIORITIZED sources were already routed last, so a clean-source outage degrades least. | Sections sourced by the down provider are thinner or absent; `data_freshness` stamps the gap; scalar Fields fall back through the Waterfall to other providers. | Provider recovers → next scheduled/triggered re-Run refills; `03 §6` freshness-TTL re-enqueue. |
| **Budget exhausted (G4)** | reserve/charge ledger shows the aggregate Dossier or per-Tenant AI ceiling reached | The cascade **stops at the free tier** (or stops with best-so-far); **no paid escalation** — spend is disposed by the deterministic gate, never by the model. | Dossier returns with best-so-far sections + `pending`; no surprise paid-token spend. | Budget window resets or the Tenant raises the `configver` budget; a re-Run then escalates. |
| **Intent lane saturated** | `intent_refresh` backlog on `job_outbox` | Intent is **async-only**: a sync preview shows **last-known intent or `pending`**, never a blocking compute; the refresh drains FIFO. | Intent tiles show `pending`/last-known age; the rest of the Dossier is unaffected (ADR-0027). | Backlog drains; scheduled sweep + webhook triggers catch up (`05 §5`). |
| **Egress-proxy pressure** | egress breaker / dial saturation | New `provider.Call`s back off under G3; reserves fail closed rather than queueing in worker memory. | Slower Run completion; `202` submissions still accepted (durable queue). | Pressure clears; the durable DAG resumes mid-Run from the `internal/durable` step log — no restart. |

Doctrinal note (verbatim from `docs/waterfall-dashboard/11 §5`): degradation is read-completeness only. The five
gates — G1 tenant isolation, G2 idempotency, G3 bounded execution, G4 cost ceiling, G5 provenance — and the single
SSRF boundary never degrade; "the model proposes, a deterministic gate disposes" holds under every mode.

## 6. What deployment does **not** add

| # | Not added | Pointer |
|---|---|---|
| 1 | No new `cmd/*` binary — the three existing deployables absorb R&I along their existing axes. | `02 §6`, ADR-0010 |
| 2 | No new infrastructure — no Redis/Kafka/ClickHouse/Temporal, no `pgvector`; DAG fan-out is the Postgres `internal/job` path. | ADR-0011/0013/0014/0029 |
| 3 | No second internet route — LLM/search/dataset (and roadmap CRM push) all traverse the single egress-proxy. | ADR-0010/0030 |
| 4 | No provider API key as an env var — keys are sealed key-pool rows injected at egress. | ADR-0017/0026 |
| 5 | No new Go dependency to deploy — LLM/search/dataset are HTTP+JSON adapters; struct validation; stdlib-only. | ADR-0016/0022/0026 |

## Open items

| ID | Item | Status | Owner |
|----|------|--------|-------|
| DP-OI-1 | Final env-var names/defaults in §2 (`RESEARCH_*`, `LLM_*`, `INTENT_*`) frozen at implementation code review | OPEN — lands with the R&I slices (`16`) | GTM Infrastructure Engineer |
| DP-OI-2 | Canary success/rollback criteria for a new LLM adapter (error-rate + cost-share guardrails before enable) | OPEN — `13` metrics wire the guardrails | Senior Backend Engineer |
| DP-OI-3 | `RESEARCH_MAX_CONCURRENT_RUNS_PER_TENANT` / step-concurrency defaults are UNVERIFIED until the `10`/`14` fleet load test | OPEN — converted by `10` load plan | Principal Backend Engineer |
| DP-OI-4 | LLM health-probe endpoint per adapter (zero-token metadata call vs minimal completion) so canary/health don't spend budget | OPEN — `13 §5` | Senior Backend Engineer |
| DP-OI-5 | Roadmap CRM push deploy (egress-proxy outbound "write" mode + CRM host allow-list), Slice 27+ | Deferred (roadmap, ADR-0030) | Staff Security Engineer |
