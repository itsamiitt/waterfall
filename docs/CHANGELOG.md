# Changelog

All notable changes to the planning + implementation of the Waterfall Enrichment Engine.
Format: reverse-chronological; group by phase; note back-propagated improvements explicitly.

## [Unreleased]

### 2026-07-11 — R&I Slice 26 (part d): operator cross-Tenant read for the R&I dashboards (migration 0017, live-green)
Closes the RBAC↔RLS gap on the two Slice 26 read-models: the rbac matrix grants the operator role
`DecisionAllow` (cross-Tenant) for `research.read`/`intent.read`, but 0015/0016 shipped only the
Class-T `*_tenant_isolation` policies — so under dual-GUC an operator (own Tenant = sentinel
`platform`) saw **zero** real-Tenant rows. **Migration `0017_ri_operator_read.sql`** adds the additive
permissive `FOR SELECT USING (app_current_role()='operator')` policy on `research_dossiers` +
`intent_scores` (RLS OR-combines permissive policies, so `tenant_admin`/`tenant_user` stay confined by
`*_tenant_isolation`; operator read is SELECT-only — no cross-Tenant write). Identical in shape to
0009's `tenant_usage_*`/0006/0007 operator-read. Both dashboard RLS integration tests extended to prove
operator cross-Tenant read **and** that a non-operator role stays fail-closed; both pass **LIVE against
PG17**, in both run orders (no ordering dependence). Migration ledger renumbered (news→**0018**,
CRM→**0019**) across the R&I doc series; ADR-0017/0018 references untouched. Full suite + `-race` green;
zero new Go dep.

### 2026-07-11 — R&I Slice 26 (part c): research dashboard read-model + HTTP + dashboardd mount (live-green)
`internal/dash/research.Service` (`List` → dossier summaries; `Dossier(id)` → full JSON) over
`research_dossiers` via `db.Store.Tx` (dual-GUC RLS). HTTP: `GET /v1/admin/research/dossiers` (list) +
`/dossiers/{id}` (get) behind a new **`ResearchRead` RBAC action** (operator allow; TA/TU own-Tenant),
mounted in `cmd/dashboardd`. Middleware unit tests + a build-tagged RLS integration test that **passes LIVE
against PG17** (tenant B sees 0 of A's dossiers by id + list, fail-closed). **OpenAPI parity kept**: both
paths added to `openapi-admin.{json,yaml}`; `TestAdminOpenAPIParity` green. Full suite + `-race` green; zero
new Go dep. **Both intent + research dashboard read-models are now live-verified**; web features + an
operator cross-Tenant policy follow.

### 2026-07-11 — R&I Slice 26 (part b): intent dashboard HTTP surface + dashboardd mount
`GET /v1/admin/intent/accounts` (list) + `/v1/admin/intent/accounts/{domain}` (per-account) on `dashboardd`,
behind the shared FeatureChain session auth + a new **`IntentRead` RBAC action** (operator allow;
tenant_admin/tenant_user own-Tenant) — mirrors `internal/dash/cost`. `cmd/dashboardd` mounts `intent.Routes`
over the dashboard `db.Store`. Middleware unit tests (401 no-principal, 403 no-role) green. **OpenAPI parity
restored**: added both paths to `docs/waterfall-dashboard/openapi-admin.{json,yaml}`; `TestAdminOpenAPIParity`
green (the no-orphan/parity gate). Full suite + `-race` green; zero new Go dep. The web feature + an operator
cross-Tenant SELECT policy are follow-ons.

### 2026-07-11 — R&I Slice 26 (part a): intent dashboard read-model service (live-green)
The first **Slice-26 (dashboards)** brick: `internal/dash/intent.Service` — a tenant-scoped read model over
`intent_scores` for the admin UI, riding the dashboard **dual-GUC RLS** seam (`db.Store.Tx` binds
`app.current_tenant` + `app.current_role` from the Principal, ADR-0020). `Account(account)` → per-class
scores (score desc); `List(limit)` → accounts with their strongest intent class. A build-tagged
(`integration`) RLS test **passes LIVE against PG17** via the dual-GUC: tenant B sees 0 of tenant A's intent
(per-account and in the list), fail-closed on no-principal. `go build/vet/test` + `gofmt` green (integration
excluded from the default suite by tag); zero new Go dep. The HTTP surface (rbac read middleware) +
dashboardd mount + web feature + an operator cross-Tenant policy are follow-on increments.

### 2026-07-11 — R&I Slice 25 (part b6): `POST /v1/intent/refresh` wired LIVE — full intent pipeline end-to-end
`POST /v1/intent/refresh` (write; auth + write-scope + drain; Idempotency-Key required) recomputes intent
for an account and returns the scores. `cmd/enrichapi` constructs the `IntentRefresher` over the **real**
seams — `EngineSignalCollector` over the engine store (`st.Current`) + `Scorer(DefaultWeights)` + the intent
store (`SaveScores`) + write-back via `st.Append`. `api.Server.IntentAPI` gains `Refresh`. Handler unit tests
(runs / missing-key / no-account / no-principal / no-refresher) green. **Live smoke** (enrichapi on PG17): a
seeded `buying_signal=hiring` Field for tenant-acme → `POST refresh` derives a hiring signal → scores **0.6**
(reasoning intact) → persists → `GET /v1/intent/accounts` reads it back → `GET /v1/records/acme.com` shows the
**write-back** (`intent_score=0.600`, provider `intent-engine`). **The full intent pipeline runs end-to-end
through the real binary + PG + RLS** — enrichment Field → signal → score → persist → write-back → readable.
Full suite + `-race` green; zero new Go dep. **Both research + intent subsystems are now complete and
live-verified end-to-end; Slices 21–25 done.**

### 2026-07-11 — R&I Slice 25 (part b5): EngineSignalCollector — intent from enrichment Fields (data source)
`internal/intent.EngineSignalCollector` implements `SignalCollector` by deriving Intent Signals from an
account's current enrichment Fields (`buying_signal` event → hiring/buying class; `funding_stage` → a
buying/funding signal), carrying provider provenance + confidence through (`source_type=api`). Unknown
values yield no signal (no fabricated intent). A v1 bridge over the engine's Field read model
(`store.FieldVersions.Current` via a `FieldReader` seam); a dedicated signal-provider collector
(job-posting volumes, technographics deltas over time) is roadmap. An **end-to-end test wires it into the
`IntentRefresher`** (Fields → `ScoreAll` → write-back), proving the full intent pipeline runs from real
enrichment data. Tests + `-race` green; full suite clean; zero new Go dep. This gives the intent engine a
working data source; the remaining wiring is a scheduled/triggered `job.Kind=intent_refresh` in enrichapi.

### 2026-07-11 — R&I Slice 25 (part b4): `intent_refresh` orchestration (async lane core)
`internal/intent.IntentRefresher` — the async lane that makes intent self-populating (ADR-0027): collect
signals (`SignalCollector` seam) → `Scorer.ScoreAll` → persist per-class scores (`ScoreWriter`) →
`Project` → write back the three canonical Fields (`intent_score`/`intent_topics`/`buying_signal`) into
`field_versions` via a `FieldWriter` seam (the engine store's `Append`; intent is the single write-back
owner, G5 provenance `provider=intent-engine`). PG-free + seam-based (unit-testable with fakes, like the
research orchestrator). Tests (persist+write-back, no-signals-writes-nothing, collector-error) + `-race`
green; full suite clean; zero new Go dep. The real `SignalCollector` (provider outputs → Signals) +
`job.Kind=intent_refresh` submission follow.

### 2026-07-11 — R&I Slice 25 (part b3): intent read API `GET /v1/intent/accounts/{domain}` mounted LIVE
The computed-intent read surface. `internal/intent.HTTPHandler` serves `GET /v1/intent/accounts/{domain}`
(auth → principal G1; an account with no intent = 200 with `[]`; no store = 404) via a `ScoreStore` seam.
`api.Server` gains an `IntentAPI` interface + mount (read: protected + rate-limited). `cmd/enrichapi`
constructs `intent.OpenStore` when Postgres is enabled (guarding a typed-nil interface) and grants `app_rls`
on `intent_scores`. Handler unit tests (scores/empty/no-store/no-principal/error) green. **Live smoke**
(enrichapi on PG17): seeded scores for `tenant-acme` are returned (hiring 0.8 first, reasoning round-tripped)
to the acme token, while the globex token sees `{"scores":[]}` — **cross-tenant RLS enforced end-to-end**
through HTTP→store→PG. Full suite + `-race` green; zero new Go dep. The async `intent_refresh` lane that
WRITES scores follows.

### 2026-07-11 — R&I Slice 25 (part b2): migration 0016 `intent_scores` + intent pgstore + RLS test (live-green)
Persistence for the computed intent engine, mirroring research. **Migration 0016** = `intent_scores`
(per-account class score + confidence + `reasoning` JSONB + pinned `config_version`;
`UNIQUE(tenant_id,account,signal_class)` upsert target), Class-T FORCE RLS matching `0001`.
`internal/intent.Store` (`SaveScores` upsert, `GetByAccount` score-desc; RLS-scoped from the ctx
principal, no BYPASSRLS). A build-tagged (`integration`) RLS test **passes LIVE against ephemeral PG17**:
all **16 migrations compose cleanly**; tenant B sees 0 of A's scores; upsert updates in place; reasoning
round-trips; fail-closed on no-principal; cross-tenant INSERT rejected by `WITH CHECK`. Full suite +
`-race` green; zero new Go dep. `GET /v1/intent/accounts/{domain}` + enrichapi wiring + the async
`intent_refresh` lane follow.

### 2026-07-11 — R&I Slice 24 (part c): dossier persistence wired + `GET /v1/dossiers/{domain}` (live-green)
`POST /v1/research` now **persists** the assembled Dossier (upsert-per-subject via `research.Store`) when
Postgres is configured, and **`GET /v1/dossiers/{domain}`** reads the freshest one back — both behind the
gateway's auth (the read is protected + rate-limited; the write adds write-scope + drain gate).
`api.Server.Research` became a small `ResearchAPI` interface (`Research` write + `Dossier` read).
`cmd/enrichapi` constructs the `research.Store` when Postgres is enabled (guarding against a typed-nil
interface) and the startup bootstrap now grants `app_rls` on `research_*`. Handler unit tests (persist-on-POST,
dossier GET hit/miss/no-store/no-principal) green. **Live smoke** (enrichapi on real PG17 as the `app_rls`
role): 15 migrations apply (incl. 0015), `POST` persists, `GET /v1/dossiers/acme.com` reads the **same**
dossier back through RLS, an unknown domain → 404. Full suite + `-race` green; zero new Go dep. This
completes the research subsystem end-to-end (assemble → persist → retrieve); the async 202+job lane is a
later refinement.

### 2026-07-10 — R&I Slice 23 (part e): research pgstore + RLS integration test (live-green)
`internal/research.Store` — a Postgres-backed store for Dossiers + queryable provenance (migration 0015),
RLS-scoped exactly like `internal/pgstore` (never takes a tenant id; binds `app.current_tenant` per tx from
the ctx Principal; the app role has no BYPASSRLS). `SaveDossier` upserts `research_dossiers` + replaces
`research_sources` (G5, `source_type`-guarded); `GetDossier` / `LatestBySubject` read within-tenant. A
build-tagged (`integration`) RLS test — the docs/21 §1 release-blocker — **passes LIVE against ephemeral
PG17**: tenant B sees 0 of A's dossiers (by id AND by subject), a no-principal ctx is rejected
(fail-closed), and a cross-tenant INSERT is rejected by `WITH CHECK`. `go build/vet/test` + `gofmt` green
(integration test excluded from the default suite by tag); zero new Go dep. The async 202+job_id / GET
wiring follows.

### 2026-07-10 — R&I Slice 23 (part d): migration 0015 — `research_*` tables (Class-T FORCE RLS, live-validated)
The persistence schema for the research subsystem: `research_runs` / `research_steps` / `research_dossiers`
/ `research_sources` (ADR-0028) — backing the async `POST /v1/research` (202+job_id), `GET /v1/research/{id}`,
`GET /v1/dossiers/{domain}`, and queryable per-value provenance (G5, `source_type` CHECK). Class-T tenant
isolation matching `0001` exactly (`app_current_tenant()` + FORCE RLS `USING`/`WITH CHECK`; `SET LOCAL
app.current_tenant` per tx; app role has no BYPASSRLS). **Live-validated against ephemeral PG17**: all 15
migrations apply cleanly in order (no collision with the dashboard schema); 4 tables with FORCE RLS + 4
isolation policies; the **G1 release-blocker passes as a NON-superuser** (tenant B sees 0 of tenant A's rows)
and a cross-tenant INSERT is rejected by `WITH CHECK`. The Go pgstore + a build-tagged RLS integration test
land next with the async-flow wiring.

### 2026-07-10 — R&I Slice 25 (part b1): intent write-back mapping (canonical Field projection)
`intent.Project` maps `[]ClassScore` → the three canonical Fields (ADR-0027): `intent_score` = the
strongest class's score, `buying_signal` = the strongest class, `intent_topics` = classes at/above a
threshold (score-desc, normalized comma-join). `Writeback.Fields()` yields the `(Field,value)` pairs for
the `field_versions` store — intent is the **single write-back owner**; the per-class breakdown stays in
`intent_scores`, never overloaded onto the single-valued Fields. Deterministic; empty scores → zero
Writeback. Tests + `-race` green; full suite clean; zero new Go dep. The store write + async
`intent_refresh` lane + `intent_*` persistence (migration 0016, live PG) follow.

### 2026-07-10 — R&I Slice 25 (part a): computed Intent Engine scoring core (`internal/intent`)
The **deterministic, auditable intent scoring core** (ADR-0027). Ten intent classes; a `Scorer` turns
collected `Signal`s into per-class scores via **decay** (`magnitude·2^(-age/halflife)`) → **weight** →
**noisy-OR corroboration** → optional **isotonic Calibrator hook**, with per-signal `Reasoning` retained
(G5, explainability) and a per-class confidence. Weights + half-lives are a versioned config
(`DefaultWeights` cold-start). Deviation (ADR-0003): `engine.fuseAgreeing`/`logit` are unexported, so v1
uses a self-contained noisy-OR combiner (0 with no evidence, monotonic in evidence); `calibrate.FitIsotonic`
is the offline backfill and log-odds Naive-Bayes is the calibrated target — all scores **UNVERIFIED** until
backtested. Tests (decay, corroboration, no-evidence=0, calibrator hook, clamp, deterministic reasoning) +
`-race` green; full suite clean; zero new Go dep. PG-free core; the async `intent_refresh` lane + write-back
into `field_versions` + `intent_*` persistence (migration 0016, live PG) follow.

### 2026-07-10 — R&I Slice 24 (part b): `POST /v1/research` mounted LIVE in enrichapi (smoke-verified)
The Research API is now **served by the real `enrichapi` binary**. `api.Server` gains an optional
`Research http.Handler` field, mounted at `POST /v1/research` behind the same posture as enrichment
(protected auth → principal G1, per-tenant rate limit, write-scope, drain gate; off by default → 404).
`cmd/enrichapi` constructs the production orchestrator — `EngineEnricher` over the live engine +
`CollectDiscoverer`(brave) + `CascadeAIRunner`(`ai.Models()`, `DefaultPrompts()`) — all over the **same**
egress client, with the SSRF host allow-list extended to the search + LLM hosts (`collect.Hosts()`/
`ai.Hosts()`, ADR-0025/0026). **Live smoke** (memory mode, no keys): the binary boots ("research API
enabled"), `POST /v1/research` returns a valid Dossier JSON, and the `processing_log` **honestly** records
each step degrading (search `PROVIDER_DOWN`/no key, 0 fields enriched, AI cascade exhausted) — **no
fabricated data**; unauth → 401. api mount test (mounted/protected/scope-gated/off-by-default) + full suite
+ `-race` green; zero new Go dep.

### 2026-07-10 — R&I Slice 24 (part a): `POST /v1/research` is callable (domain → dossier over HTTP)
**The headline endpoint.** `internal/research.HTTPHandler` serves `POST /v1/research` reusing the platform
API conventions (ADR-0012): tenant from the authenticated Principal (G1, never the body), `Idempotency-Key`
required on writes, snake_case JSON, uniform error body `{"error":{"code","message"}}`. This increment
serves the **synchronous** assembly (the `?mode=sync` preview) — it runs the orchestrator inline and returns
the Dossier JSON. An **end-to-end test drives HTTP → real orchestrator → real enrichment engine** (in-memory
store + mock provider) + a stub AI and asserts a Dossier with provenance; error cases (missing key 400, no
identifiers 422, no principal 401, unknown field 400) covered. The handler is injected via an `Assembler`
seam, ready to mount into `cmd/enrichapi`. The default async 202+job_id flow + `GET /v1/research/{id}` land
with persistence (migration 0015). Tests + `-race` green; full suite clean; zero new Go dep.

### 2026-07-10 — R&I Slice 23 (part c): enrichment seam over engine.Run (real-seam trio complete)
`EngineEnricher` adapts the enrichment engine (router `Plan` + `engine.Run` under G1–G5) to the
orchestrator's `Enricher` seam — mapping a Subject → `EnrichmentRequest` for the canonical Fields and the
`Outcome` back to per-Field values + provenance (tenant isolation G1 from the ctx Principal; G4 ceiling; G2
idempotent re-run). This **completes the real-seam trio** (Discoverer/`collect` + AIRunner/`ai`-cascade +
Enricher/`engine`): a test runs the orchestrator against the **real** engine (in-memory store + mock
providers) + a stub AI and asserts a Dossier with `source_type=api` provenance for engine-filled Fields.
PG-free. Tests + `-race` green; full suite clean; zero new Go dep. Remaining slice 23: persistence —
migration 0015 (`research_*` FORCE RLS) + pgstore + RLS zero-rows (live PG) — then slice 24 `POST /v1/research`.

### 2026-07-10 — R&I Slice 23 (part b): real ai/collect seams wired into the orchestrator
`CascadeAIRunner` (over `ai.RunCascade` + per-task struct validators + a `PromptStore` of content-trust-safe
default prompts — collected text is untrusted data, never instructions) and `CollectDiscoverer` (over
`collect.Client`) bridge the orchestrator to the real AI and search clients — both PG-free, so verifiable
now. An **end-to-end test** wires the orchestrator to a real `collect` Brave client (httptest) + a stub
completer + a fake enricher and asserts a complete Dossier assembles (profile, AI summary, merged search
keywords, typed provenance). The enrichment seam over `engine.Run` + persistence (`research_*` /
migration 0015, needs live PG) are the next increment. Tests + `-race` green; full suite clean; zero new Go dep.

### 2026-07-10 — R&I Slice 23 (part a): research orchestrator core + Dossier schema (`internal/research`)
The **domain→Dossier assembly core** (ADR-0028): a **deterministic DAG orchestrator** that composes the
discovery (`internal/collect`), enrichment (engine seam), and AI (`internal/ai` cascade) seams in a
**fixed order** — the orchestrator, never a model, chooses the steps (ADR-0026). The `Dossier` response
schema mirrors doc 06 + `openapi-research.json`: firmographics reference canonical Field values while
competitors/news/etc. are Dossier-only, and every value carries a `Source` with
`source_type ∈ {api,dataset,ai_inference}` (AI values kept provenance-distinct, never fused as facts).
Intent is async → `pending` on a sync assembly (ADR-0027). In-memory + seam-injected (unit-testable with
fakes); persistence (`research_*` / migration 0015), `internal/job` wiring, and the G2 idempotency cache
land in the next increments. Tests (assembly/provenance/deterministic-order/error-resilience) + `-race`
green; full suite clean. Zero new Go dep.

### 2026-07-10 — R&I Slice 22 (part 2): search collection layer (`internal/collect`)
The data-collection **search** client (ADR-0025): **Brave / Tavily / Serper** as bounded, breaker-guarded
egress calls reusing the egress key-injection seam (`provider.WithAuthDescriptor`). Search returns
**discovery** (URLs + snippets), NOT Fields, so — generalizing deviation **D-1** — it is a dedicated client,
not a Field-shaped adapter, and search Providers are a separate `Providers()` registry never wired into the
enrichment engine. Per-dialect request build + response parse; a returned URL is discovery-only (resolved
only via another provider API — the ADR-0025 boundary). Inclusion status per ADR-0009: Brave (own index)
**ACTIVE-CANDIDATE**; Serper/Tavily (SERP-derived) **DEPRIORITIZED** (RI-OI-1). Zero new Go dep; tests
(per-dialect + auth injection + status classification) + `-race` green; full suite regression-clean.
Remaining slice 22: **dataset** providers that fill canonical Fields (normal HTTPAdapter; GLEIF already exists).

### 2026-07-10 — R&I Slice 22 (part 1): 6 canonical Fields (33→39) + Brandfetch social coverage
Code catches up to the DOC-FIRST field registration (ADR-0028): the six single-valued R&I scalars
(`twitter_url`, `facebook_url`, `github_url`, `crunchbase_url`, `company_ticker`, `total_funding_usd`)
are added to `internal/domain/field.go` (const block + `canonicalFields` map); `Valid()` now accepts
**exactly 39**, locked by a new `internal/domain` test. **Brandfetch** (social URLs from `links[]`) and
**Crunchbase** (`funding_total.value_usd`→total_funding_usd, `stock_symbol`→company_ticker,
`permalink`→crunchbase_url) give **all six** new Fields real provider coverage (fixtures + `wave0_test`
extended, subset-semantics runner). Multi-valued R&I data (competitors, funding rounds, …) stays
Dossier-only, never a Field. `go build/vet/test` + `gofmt` green; full suite regression-clean.
Remaining slice 22: search/dataset providers (Brave/Tavily/Serper/OpenAlex/SEC-EDGAR/Common Crawl index)
— search returns discovery/URLs, not Fields, so it needs a dedicated collection client (generalizes D-1).

### 2026-07-10 — R&I Slice 21 (part 1): LLM egress client + deterministic cost cascade
First implementation slice of the Research & Intelligence platform
(`docs/research-intelligence/16-implementation-phases.md`). LLM inference as a bounded, cost-metered
egress call — reusing the enrichment egress/key-injection/breaker machinery with **zero new Go
dependency**; all gates green.
- **`internal/ai`**: Model registry (openrouter free-first + openrouter-paid/openai/anthropic);
  dual-dialect (OpenAI + Anthropic) `LLMClient` with egress key-injection + a per-model circuit breaker
  (G3); struct-based stdlib JSON validation (no schema engine); and the **deterministic free→paid Model
  Cascade** — accept/escalate/stop disposed ONLY by schema-valid + G4 budget + attempt count (never a
  model's self-reported confidence, never a model-chosen tool; losers retained for G5).
- **`internal/provider`**: exported the egress seam (`WithAuthDescriptor`, `ClassifyStatus`) so a
  non-`HTTPAdapter` caller reuses key injection + the status taxonomy; backward-compatible, every
  existing adapter/test untouched.
- **Deviation D-1** (recorded in `04-ai-pipeline.md`): LLM uses a dedicated client + a separate registry,
  not the Field-shaped `HTTPAdapter.Fetch`. `go build/vet/test` + `gofmt` + `-race` all green; full-suite
  regression-clean.
- **Remaining in slice 21:** `internal/dash/airouting` (`ai_prompt`/`llm_route` config kinds over
  `configver`) + the LLM catalog projection.

### 2026-07-09 — Research & Intelligence platform: planning series + 6 ADRs (design-only, plan-first)
Opened the plan-first design series that evolves the engine into an Enterprise Research & Intelligence
platform (domain → Dossier; computed intent; AI research), reusing the enrichment core, ~145-adapter
Provider machinery, durable queue/worker fabric, RLS multi-tenancy, and the admin dashboard. **Docs +
ADRs only — no production code** (per ADR-0003; implementation is a later gate). Resolves four approved
forks and the cross-cutting conflicts an adversarial architecture review surfaced (one global migration
ledger 0015–0018; one ADR map; one owner per table; deterministic-gate-disposes for AI; single egress
preserved; stdlib-only audit).
- **6 new ADRs (0025–0030):** 0025 data-collection — admits third-party **search APIs** (Brave/Tavily/
  Serper) + public **bulk datasets** (Common Crawl *index*, OpenAlex, SEC EDGAR, GLEIF) as legitimate
  server-side Providers while browser automation/DOM scraping stay permanently banned (**supersedes
  0002**, refines 0009; returned-URL boundary; Common Crawl index-only, WARC deferred). 0026 LLM-as-
  egress-adapter + deterministic free→paid cost cascade (**zero new Go dep**). 0027 computed-intent
  methodology (signal→decay→log-odds fuse→isotonic calibrate→guardrailed score; async-only; supersedes
  the ingest-only framing of `docs/14`). 0028 research-dossier API + six single-valued Fields (33→39,
  DOC-FIRST); multi-valued data stays Dossier-only. 0029 embeddings/RAG deferred (guards zero-dep +
  free-first). 0030 CRM outbound through the **single** egress-proxy (roadmap; preserves 0010).
- **`docs/00 §7`:** registered the 6 new canonical scalar Fields (`twitter_url`, `facebook_url`,
  `github_url`, `crunchbase_url`, `company_ticker`, `total_funding_usd`) + the multi-valued-is-Dossier-
  only note. **`adr/README.md`** index updated; **`adr/0002`** marked superseded-by-0025 (Status line
  only — Decision unchanged, append-only rule).
- **New series `docs/research-intelligence/`** (00-overview anchor + 01–16 + `openapi-research.json`),
  modeled on `docs/waterfall-dashboard/`. New Mermaid diagrams: `diagrams/research-pipeline.mmd`,
  `intent-flow.mmd`, `ai-orchestration.mmd`. Implementation is scoped as Slices 21–27 (docs 43–49) in
  `16-implementation-phases.md`, not yet built. All scale/cost/perf numbers carry **UNVERIFIED**.

### 2026-07-08 — Wave 13: remaining 200-sheet rows triaged (133 → 135)
Researched the 20 remaining spreadsheet rows that plausibly had a self-serve API. Honest outcome:
**2 implemented, 7 deferred, 11 EXCLUDED** — most of the "big name" contact DBs have real APIs but
do NOT publish their response schemas (behind logins/enterprise gates), so building an accurate
Decode would mean fabricating field names, which this rollout never does.
- **Implemented:** `evaboot` (DEPRIORITIZED; clean OpenAPI email-finder, LinkedIn/scraping provenance),
  `amplemarket` (DEPRIORITIZED; submit→poll people-enrichment — the ONE big DB with a fully rendered
  response example, so its envelope is confirmed).
- **Deferred (real API, but response/request schema unverifiable without a live enterprise key, or a
  mandatory workspace-config param):** `zoominfo` (OAuth2 GTM; outputFields mechanism + response
  wrapper inferred), `cognism` (enrich→redeem; field keys inferred from CRM-mapping docs),
  `seamless-ai` (OpenAPI exists but poll-response envelope unconfirmed), `lead411` (schema fully
  inferred + non-standard login→JWT→query-param auth), `leadiq` (GraphQL; searchPeople input schema
  not captured), `databar` (mandatory workspace `waterfall_id` + per-waterfall variable schema),
  `octave` (mandatory workspace `agentOId`).
- **EXCLUDED (no self-serve API / enterprise-portal-only / no public API):** swordfish, bookyourdata,
  sales-rocks, truemail, netline, demandscience, techtarget, intentsify, rollworks, persana, g2-stack.

### 2026-07-08 — Wave 12 (part 3, final): nymblr + kendo (131 → 133); Wave 12 closed
- **nymblr** (DEPRIORITIZED, bearer): US B2B contact database (POST /append/contact) — a full
  24-field person+company enrich; marked DEPRIORITIZED per ADR-0009 (public-web/LinkedIn PII
  provenance, consistent with Apollo/Lusha/ContactOut). Overlapping schema fields (personalEmail vs
  contactPersonalEmail, companySic vs companySICCode6) mapped best-fit + down-weighted.
- **kendo** (ACTIVE-CANDIDATE, api-key-query): email finder by name+domain (/emailbyname → work_email
  + private_email→personal_email). Errors carry no JSON body (status line only); 405 reused for
  out-of-credit (documented discrepancy).

**Wave 12 final tally (12 researched):** 9 implemented (ares-cz, cro-ie, sendgrid-validation, proofy,
captainverify, data8-phone, charity-commission-uk, nymblr, kendo), 2 deferred (sec-edgar — needs a
ParseSubmit-with-request + bulk-file cache; cvr-dk — http-only endpoint incompatible with the
https-only egress), 1 excluded (kbo-be — no REST/JSON API). Registry now at **133 adapters**.

### 2026-07-08 — Wave 12 (part 2): verifiers + phone + charity register (126 → 131)
- **sendgrid-validation** (bearer, dedicated Email-Validation key): result.verdict Valid|Risky|Invalid.
- **proofy** (api-key-query): reads status at $.status OR $.result.status (documented schema/example
  inconsistency handled defensively).
- **captainverify** (api-key-query): 200-with-success=false error convention; credit exhaustion
  degrades to result="unknown" (not an error).
- **data8-phone** (api-key-query, POST body): Status.Success=false in-body error; phone_status from
  ValidationResult+NumberType; NoCoverage/Ambiguous → inconclusive (omitted).
- **charity-commission-uk** (DEPRIORITIZED, Ocp-Apim-Subscription-Key): implemented single-shot
  against the OFFICIALLY-VERIFIED searchCharityName response only (charity_name, date_of_registration
  → founded-year proxy, constant company_type="charity") — the detailed-fetch fields were UNVERIFIED
  camelCase from an unofficial client with mismatched routes, so deliberately NOT mapped.

### 2026-07-08 — Wave 12 (part 1): Czech ARES + Ireland CRO registries (124 → 126)
Final-sweep registries batch 1 — 2 implemented, 2 deferred, 1 excluded:
- **ares-cz** (ACTIVE-CANDIDATE, AuthNone): official Czech Ministry of Finance register; POST
  /ekonomicke-subjekty/vyhledat by name (live-verified no-auth); coded legal-form/CZ-NACE stored as
  codes.
- **cro-ie** (DEPRIORITIZED, Basic auth "<email>:<api-key>"): Ireland CRO Open Services; bare-array
  response; register fields only (name/type/reg-date).
- **DEFERRED sec-edgar** — the name→CIK match needs the ~1MB company_tickers.json resolved
  client-side against the *input name* (needs both request + fetched file — an async shape the
  current match→fetch doesn't express) and covers only SEC-registered public filers.
- **DEFERRED cvr-dk** — every source documents `http://distribution.virk.dk` (TLS UNVERIFIED); the
  egress SSRF gate is https-only, so an http-only endpoint is un-callable. Also: 3-week manual
  credential approval + Elasticsearch query DSL.
- **EXCLUDED kbo-be** — Belgium CBE/KBO exposes no REST/JSON API (SOAP web service + monthly file
  download only); matches ADR-0002/0009 exclusion criteria.

### 2026-07-08 — Wave 11 (part 4, final): mailboxlayer + Melissa + Loqate (121 → 124)
Wave 11 closes at 12/12 researched — 11 implemented, 1 excluded (abn-lookup):
- **mailboxlayer** (APILayer legacy host apilayer.net, access_key query): ALL errors are HTTP 200
  with {success:false,error:{code}} (live-verified) — classified by numeric code (101/102 auth,
  104 quota, 106 rate-limit, 999 transient); boolean smtp_check → valid|invalid; echoed email
  classified work vs personal by the free/disposable flags.
- **melissa-global-phone** (license key as "id" query; official OpenAPI spec): verdict is the
  comma-delimited Records[].Results code string — PS01 = valid (PS08 → landline), absence = invalid;
  request-level failures arrive as a non-empty TransmissionResults inside HTTP 200 (specific GE
  codes UNVERIFIED — any non-empty value treated as AUTH-class).
- **loqate-phone** (GBG; "Key" query): Items-wrapped error envelope checked before success fields
  (legacy paths return errors under HTTP 200 — live-verified); IsValid is a STRING Yes|No|Maybe —
  "Maybe" yields no phone_status (inconclusive), Yes maps NumberType through the normalized vocab.

### 2026-07-08 — Wave 11 (part 3): NZ registry + verifiers (118 → 121) + TokenFromRequest
- **ADR-0024 extension: `AsyncHTTPAdapter.TokenFromRequest`** — derives the poll token from the
  ORIGINAL request when the submit body carries no job id (ParseSubmit, if set, still validates the
  submit body). First consumer: SendPulse, whose status endpoint is keyed by the submitted email.
- **sendpulse-verifier** (async, oauth2-cc JSON token style; pool "<client_id>:<client_secret>"):
  paired send-single-to-verify → get-single-result?email=; {"result":false} = pending.
- **nz-companies** (official MBIE NZBN v5, match→fetch): search-term → /entities/{nzbn};
  Ocp-Apim-Subscription-Key header; live-verified field names (Xero); ANZSIC description mapped to
  industry (code NOT mapped to naics/sic); city rides in address3 by NZBN convention (~0.65).
- **verimail** (single-shot, key query): result enum incl. inbox_full/hardbounce/softbounce; in-body
  status success|error checked independently of HTTP code; 403 = quota (documented discrepancy).

### 2026-07-08 — Wave 11 (part 2): registry aggregators (116 → 118) + ABN Lookup exclusion
- **north-data** (DEPRIORITIZED — clean OpenAPI'd European-register data, but manual key issuance at
  €500/mo minimum): `/company/v1/company?name=&fuzzyMatch=true&financials=true&extras=true`;
  X-Api-Key header; NACE/NAICS codes mapped (uksic deliberately NOT mapped to sic — UK SIC ≠ US SIC);
  financial indicator ids matched case-insensitively (docs conflict on "Revenue" vs "revenue").
- **opensanctions** (DEPRIORITIZED — sanctions/PEP screening, near-zero hit rate for ordinary B2B;
  optional compliance screen): POST /match/default (FollowTheMoney arrays, schema constant
  "Company"); auth header value is literally "ApiKey <key>" (pool secret holds the full value);
  values accepted only when the API asserts match==true, confidence scaled by its score.
- **EXCLUDED: abn-lookup** — the JSON interface is JSONP-only (verified live: callback wrapper even
  with no callback param, Content-Type text/javascript); the only alternative is SOAP/XML. Matches
  the ADR-0002/0009 exclusion criteria; recoverable if wrapper-stripping is ever permitted.

### 2026-07-08 — Wave 11 (part 1): official open-data registries (113 → 116) — first VERIFIED shapes
Three free, no-credential government/official registries implemented on the new AuthNone scheme
(egress passthrough, migration 0014). Because they are public APIs, the researcher verified the wire
shapes LIVE — these are the rollout's first fixtures marked **VERIFIED** rather than UNVERIFIED:
- **brreg** (Norway Brønnøysund Enhetsregisteret, match→fetch): navn search → `/enheter/{orgnr}`;
  Norwegian keys (navn, antallAnsatte, naeringskode1, stiftelsesdato, forretningsadresse); zero-match
  = 200 with `_embedded` absent; 410 Gone = legally removed (purge caches).
- **gleif** (GLEIF LEI Records, global): `filter[entity.legalName]` search (JSON:API); legalForm.id
  is an ISO 20275 ELF code (documented as code-not-label); no-match = 200 empty data[].
- **recherche-entreprises** (French DINUM/SIRENE): `/search?q=`; NAF/APE + INSEE codes documented as
  code-valued; hq_country = constant "FR" (France-only registry, cited). DELIBERATELY not mapped:
  tranche_effectif_salarie (band-code semantics conflicted in research — needs a verified INSEE
  decode) and dirigeants names (company officers, not the enrichment subject).
Remaining Wave-11 research (9 providers) still in flight. `go build ./...` + `go test ./...` green.

### 2026-07-08 — Wave 10: +10 provider adapters (103 → 113)
Twelve more providers researched (cited); 10 implemented, 1 deferred, 1 excluded:
- **Email verify**: cloudmersive (bare-JSON-string body), abstract-email, mailercheck, reoon,
  mails-so ({data,error} envelope), emailhippo (api-key-PATH — second AuthAPIKeyPath consumer),
  truelist (query param on POST).
- **Phone validate**: neutrinoapi (dual-header User-ID+API-Key; kebab-case keys).
- **Firmographics**: bigpicture (raw key as whole Authorization value; 202 = queued re-request).
- **Identity** (DEPRIORITIZED, public-records provenance): enformion (dual-header galaxy-ap-* +
  static galaxy-search-type routing header; 200-with-isError body).
- **Deferred**: sinch — v2 endpoint needs an account-specific {projectId} URL segment (mandatory
  config, same blocker class as Enlyft's solution_id).
- **EXCLUDED**: findthatlead — vendor's own help center: "Sorry, we don't have an API available."

All reuse existing auth schemes (incl. both ADR-0024 Phase-4 variants) — no migration. Each has a
fixture + wave-test + registry entry. `go build ./...` + `go test ./...` green; registry invariants
(seed parity, SSRF hosts, field coverage) hold at 113 adapters.

### 2026-07-07 — Wave 9: +13 provider adapters beyond the 200-tool sheet (90 → 103)
Researched (cited) and implemented 13 further real-API providers, expanding coverage past the
reconciled 200-tool spreadsheet, and resolved the last async deferrals:
- **Email verify** (single-shot): quickemailverification, myemailverifier, mailboxvalidator, bouncify,
  emaillistverify (JSON *Detailed* endpoint).
- **Phone validate** (single-shot): trestle (`/3.0/phone_intel`), numlookupapi (number in path).
- **Firmographics**: companyenrich (bearer; bucketed size/revenue + funding_stage + naics/tech arrays);
  **companies-house** (UK official/free) as a match→fetch async adapter (search → `/company/{n}`).
- **Identity** (DEPRIORITIZED, LinkedIn provenance): enrich-so (`POST /api/v3/reverse-lookup/lookup`).
- **Revisited async deferrals now implemented**: surfe + lemlist (submit→poll), voila-norbert (single-shot).

Each is secret-free (`AuthDescriptor` only), maps only canonical Fields, carries a `_found.json`
fixture + wave-test case (sync in `TestWave0_DecodeFixtures`, async in `TestAsyncWave_SubmitPoll`),
and a registry entry. Basic-auth providers document the exact pool-secret form (`"<key>:"`, `":<key>"`,
`"any:<token>"`) since egress base64-encodes the pool secret verbatim. All 13 reuse existing auth
schemes — no new migration. `go build ./...` + `go test ./...` green; catalog-seed parity + field
coverage + SSRF host-coverage invariants still hold at 103 adapters.

### 2026-07-07 — Live-Postgres verification + fix: migration 0013 (provider auth schemes)
Ran the ADR-0023 seeder against a live Postgres (Neon): all 13 migrations apply cleanly, and
`cmd/providerseed` projects all **90 adapters into the `providers` catalog (one row each)** —
inserts succeeding under FORCE RLS via the platform-tenant context (write-path RLS verified). The
run **caught a real schema/code drift**: the migration-0005 `providers_auth_scheme_check` predated
the ADR-0024 egress schemes, so seeding `tomba` failed (23514 — `api-key-dual-header` rejected).
**Fix: migration `0013_provider_auth_schemes.sql`** widens the constraint to include `api-key-path`
and `api-key-dual-header`; re-seed then completed 90/90. Added a regression guard in
`TestSeedInputFor_AllRegistered` — every adapter's auth scheme must be in the catalog-accepted set,
turning this drift into a build failure. (The read-path RLS integration test — non-superuser sees
only the tenant_readable projection — can't run on Neon: it password-authenticates a SQL-created
role, which Neon's managed-role model rejects; it still runs on standard Postgres/CI.) `go build
./...` + `go test ./...` green.

### 2026-07-07 — Verification: full canonical-field provider coverage
Added `TestRegistry_FieldCoverage` — asserts every one of the 33 canonical `domain.Field`s is
advertised by ≥1 registered adapter, so the router can satisfy a request for any Field and a
vocabulary field with no provider fails the build. All 33 are covered by the 90 adapters (e.g.
funding_stage via crunchbase/coresignal/oceanio, duns_number via dnb, intent_*/buying_signal via
6sense). A curated `essential` subset is checked first for precise regression messages. `go build
./...` + `go test ./...` green. This closes the concrete, autonomously-doable Verification gaps
(async-through-engine, catalog-seed parity, SSRF host coverage, field coverage).

### 2026-07-07 — Verification: egress SSRF allow-list covers all 90 adapters
Added `TestRegistry_HostsCoverAllAdapters` — proves the SSRF allow-list the binaries build from
`adapters.Hosts()` admits every registered adapter's base host **and** every oauth2-cc `TokenURL`
host (the token exchange runs through the same SSRF-checked base transport). A provider whose host
were missing would have all its calls — or its token exchange — silently refused at egress and be
un-callable; this makes that a build-failing invariant. Also asserts the list rejects an unlisted
host (it's a real filter, not permit-all). `go build ./...` + `go test ./...` green.

### 2026-07-07 — Verification: catalog-seed parity for all 90 adapters
Added `TestSeedInputFor_AllRegistered` (cmd/providerseed) — asserts EVERY registered adapter,
including the `NewAsync` entries and the dual-header / oauth2-cc / api-key-path auth variants (which
reach the seeder via `Registered.Construct` → `provider.Introspectable`), projects to a well-formed,
catalog-insertable `SeedInput`: matching id, seedable ADR-0009 status, https base host, ≥1 canonical
capability, non-empty auth scheme, display name, unit cost. Catches ADR-0023 registry↔catalog drift
(a missing base host = SSRF-refused calls; a non-canonical cap = silently dropped) without a Postgres
test DB. `go build ./...` + `go test ./...` green.

### 2026-07-07 — Verification: ADR-0024 async path proven through the engine
Added `TestAsyncAdapter_EngineIntegration` — drives a registered submit→poll adapter (Enrow) through
the full Router→Engine→Store spine, proving the async path end-to-end (not just in isolation): the
engine's `policyFor` selects the adapter's longer *bounded* budget (its AsyncHTTPAdapter CallPolicy
override, not the 3s default), the internal submit→poll loop resolves the email inside one
`provider.Call`, and the terminal value lands in the G5 provenance store with a committed cost (G4).
Closes the gap where async adapters + policy-selection were only tested separately. `go build ./...`
+ `go test ./...` green.

### 2026-07-07 — Wave 8 residual-row audit (90 adapters)
Verified the ~15 spreadsheet rows Wave 7 had dismissed without cited research. Added 7 sync
adapters: **uplead**, **adapt-io** (dual-header), **aeroleads** [L2 email-find]; **scrubby**,
**enrichley**, **mailfloss** [L3 verify]; **extruct** [L6 firmo]. EXCLUDED with citations (docs/03):
Datanyze (ZoomInfo, Chrome-ext only), Persana AI (MCP/agent), Octave (ICP-fit not enrichment), Rift
(AI-SDR), BookYourData (no documented API), Leadyfy (no product). Deferred: Surfe, Lemlist (async,
poll path unverified), Autobound (enrich response schema unverified). `go build ./...` +
`go test ./...` green (each adapter has a wave0 fixture-decode case). 90 real adapters — the
spreadsheet is now fully reconciled with *cited* verdicts for every row.

### 2026-07-07 — Wave 7 coverage-audit gap-fill (83 adapters)
A diff of the actual 200-tool spreadsheet (sheet1 Tool column) against the registry caught a missed
L2/L3 long-tail — earlier "rollout complete" was premature. Added 8 sync adapters: **leadmagic**,
**getprospect**, **skrapp**, **tomba** (dual-header), **cufinder** (form-encoded /tep) [L2 email-find];
**bounceban** [L3 verify]; **realphonevalidation** [L5 phone-validate]; **abstract-company** [L6
firmographics]; **reverse-contact** [L1 identity, DEPRIORITIZED — reverse-email→person]. EXCLUDED
(docs/03): FindThatLead (no API — Zapier-only), TrueMail (defunct → redirects to GetProspect).
Deferred: Voila Norbert (webhook-only async, no poll endpoint). `go build ./...` + `go test ./...`
green (each new adapter has a wave0 fixture-decode case). 83 real adapters.

### 2026-07-07 — InfobelPRO + oauth2 password grant (74 adapters) — rollout closeout
Added **infobelpro** (L6 firmographics, ACTIVE-CANDIDATE) — single-shot `POST /api/search`
(`returnFirstPage=true`) authed by a new oauth2-cc **password-grant** TokenStyle (form-encoded
`grant_type=password&username&password`, pool secret "username:password"). The oauth2 injector now
covers all four token styles (basic/body/json/password). Test `TestInfobelPRO_PasswordGrant` (token
exchanged once + firmographics decoded). **This is the last cleanly-implementable provider** — the
200-provider rollout is complete: 74 real adapters spanning L1–L9; every remaining spreadsheet entry
is documented EXCLUDED (ADR-0002/0009) or a live-key-gated deferral (Cognism, Bombora). `go build
./...` + `go test ./...` green.

### 2026-07-07 — ADR-0024 complete: Cleanlist/Demandbase/PredictLeads + Phase 4b (73 adapters)
Final deferred-batch research (cognism, cleanlist, bombora, demandbase, infobelpro, predictleads) →
implemented 3: **cleanlist** (L6, company endpoint, sync Bearer — person/bulk deferred, stateful
lead_list_id), **demandbase** (L6, match→fetch + oauth2-cc **JSON** token style, ACTIVE-CANDIDATE),
**predictleads** (L7, single-shot, **two-header** `X-Api-Key`+`X-Api-Token`). This completes
**ADR-0024 Phase 4b** (`AuthAPIKeyDualHeader` + `AuthDescriptor.SecondHeaderName`) and adds oauth2
TokenStyle "json" + `accessToken` response parsing — so **all ADR-0024 phases (1–4) are now
implemented**. Deferred (documented docs/03): **Cognism** (base host unconfirmed + redeem schema
fully inferred), **Bombora** (partner-gated batch Surge report, DEPRIORITIZED), **InfobelPRO**
(needs oauth2 password-grant + search flow — next). Tests: `TestAuthInjector_OAuth2CC` (now covers
json/basic/body), demandbase in the async table, cleanlist + predictleads in wave0. `go build ./...`
+ `go test ./...` green.

### 2026-07-07 — Async wave complete: BetterContact/FullEnrich/Wiza/RocketReach (70 adapters)
Wired the final 4 submit→poll providers: **bettercontact** + **fullenrich** (L9 waterfall
orchestration aggregators, ACTIVE-CANDIDATE), **wiza** + **rocketreach** (L2 contact finders,
DEPRIORITIZED — LinkedIn provenance). All `AsyncHTTPAdapter`s with `NewAsync` registry entries;
each handles non-success terminal states (FINISHED/terminated/finished/complete = done;
CREDITS_INSUFFICIENT→QUOTA; failed→empty-terminal). Extended `TestAsyncWave_SubmitPoll` (+4 cases,
now 11 async providers). **The entire 12-provider async wave is done** — none EXCLUDED. `go build
./...` + `go test ./...` green. Only ADR-0024 Phase 4b (two-header creds, PredictLeads — unresearched)
remains deferred.

### 2026-07-07 — Async wave cont.: Snov/Explorium/Endole/SignalHire (66 adapters)
Completed the distinct-shape async providers: **snov** (L2, submit→poll + **oauth2-cc body-form**
creds — generalized the Phase-2 injector with `AuthDescriptor.TokenStyle` "body" vs "basic"),
**explorium** (L6, match→fetch, business_id token in the enrich BODY, employee/revenue as min-max
ranges), **endole** (L6, match→fetch, UK Companies House, Basic `appId:appKey`, token in the fetch
PATH), **signalhire** (L2 DEPRIORITIZED, actually **single-shot** via `withoutWaterfall=true` — its
async mode is callback-only with no poll endpoint — a plain HTTPAdapter with a top-level-array
response). `AuthInjector` oauth2-cc now supports both Basic (D&B) and body-form (Snov) token
exchange. Tests extended (`TestAsyncWave_SubmitPoll` +3 cases incl. token routing; SignalHire in the
wave0 fixture-decode table). `go build ./...` + `go test ./...` green. Remaining async: bettercontact,
fullenrich, wiza, rocketreach (vanilla submit→poll).

### 2026-07-07 — Async wave: 4 submit→poll email finders/verifiers (62 adapters)
Wired the first submit→poll `AsyncHTTPAdapter` consumers from the async-wave research: **verifalia**
(L3 email-verify, basic auth, `POST /email-validations`→poll `overview.status`), **dropcontact**
(L2, `X-Access-Token`, `POST /enrich/all`→poll `success`), **icypeas** (L2, raw `Authorization`,
poll token in the POST body, status enum DEBITED/…/NONE), **enrow** (L2, `x-api-key`,
`POST /email/find/single`→GET `?id=`, qualification ongoing/valid/invalid) — all ACTIVE-CANDIDATE,
clean API-first. Registered via `NewAsync`; each maps work_email/email_status + identity/company
fields. Table test `TestAsyncWave_SubmitPoll` drives submit→token→poll-terminal→decode for all four.
`go build ./...` + `go test ./...` green.

### 2026-07-07 — Task #8 Phase 4a: path-segment key + MixRank (58 adapters)
`AuthInjector` now handles `AuthAPIKeyPath` (ADR-0024 Phase 4a): the adapter's Build writes a
letters-only `PathPlaceholder` sentinel where the key belongs; the injector substitutes the leased
key into `URL.Path` (adapter still holds no secret). First consumer: **MixRank** (`mixrank`, L6
firmographics, DEPRIORITIZED) — `GET api.mixrank.com/v2/json/{key}/companies/match`, key as a path
segment; fills name/domain/employees/industry/SIC/NAICS/founded/hq/type/LinkedIn. Tests
`TestAuthInjector_APIKeyPath` (leased key lands in the path) + a MixRank fixture-decode case.
`go build ./...` + `go test ./...` green. Only Phase 4b (two-header credential for PredictLeads)
remains deferred.

### 2026-07-07 — Task #8: first async provider — Dun & Bradstreet (57 adapters)
Wired **D&B Direct+** (`dnb`, L6 firmographics, ACTIVE-CANDIDATE) — the first `AsyncHTTPAdapter`,
exercising all three ADR-0024 phases at once: **match→fetch** (cleanseMatch by name/country/domain →
top-candidate DUNS → data-block by DUNS), **oauth2-cc** (token exchanged at `/v2/token`, cached and
injected as Bearer on both round-trips), and a **30s bounded budget** (PolicyOverrider). Fills the
genuine **DUNS** + name/domain/hq/employees/revenue/SIC/industry; empty match → NOT_FOUND (refund +
failover), data-block never hit. To carry async adapters, the registry now holds `New` **or**
`NewAsync`, and `All`/`Hosts`/the seeder/invariant-test route through a `Registered.Construct` helper
returning `provider.Introspectable` (new interface — `Base()`+`AuthDescriptor()`, satisfied by both
`HTTPAdapter` and `AsyncHTTPAdapter`); all 56 existing entries unchanged. Tests
`TestDNB_MatchFetchOAuth2` + `TestDNB_NoMatch`. `go build ./...` + `go test ./...` green.

### 2026-07-07 — Task #8 Phase 3: AsyncHTTPAdapter (ADR-0024)
New `provider.AsyncHTTPAdapter` — a multi-round-trip adapter for **submit→poll** (Dropcontact,
Icypeas, Enrow, Wiza, SignalHire, BetterContact, Verifalia batch, InfobelPro) and **match→fetch**
(D&B cleanseMatch→data-block, Explorium, Endole; the degenerate one-poll case). It holds no secret
(each round-trip carries only the AuthDescriptor; the egress injector — incl. Phase-2 oauth2-cc —
places the credential), implements `PolicyOverrider` for a longer *bounded* budget (default 60s /
1 attempt), and its poll loop honours ctx cancellation/deadline on every sleep (never sleeps past
`ctx.Done()`), so G3 holds. Error taxonomy mirrors `HTTPAdapter` (SSRF→BAD_REQUEST, deadline→
TRANSIENT, `classifyStatus` on non-2xx, preserves classified in-body errors from ParseSubmit/Decode).
Tests: `TestAsyncHTTPAdapter_SubmitPoll` (pending→done loop + key injected on every hop),
`_PollBudgetExpires` (bounded — unfinished job abandoned at the deadline, TRANSIENT), `_PolicyOverride`.
`go build ./...` + `go test ./...` green. **Task #8 Phases 1–3 done** — the async/multi-credential
egress foundation is complete; real async providers (D&B, verifalia, dropcontact, …) can now be
wired on top.

### 2026-07-07 — Task #8 Phase 2: oauth2-cc token exchange (ADR-0024)
`AuthInjector` now handles the `oauth2-cc` scheme (previously declared but unhandled): on first use
for a pool it exchanges the pool secret (`clientId:clientSecret`) at `AuthDescriptor.TokenURL`
(POST `{"grant_type":"client_credentials"}` + `Basic` header), **caches** the `access_token` until
shortly before expiry (handles both `expiresIn` camelCase and `expires_in`), and injects
`Authorization: Bearer <token>` on the data call. The exchange runs through the base (SSRF-checked,
non-re-entrant) transport, so the TokenURL host must be allow-listed; the mutex-guarded cache is
shared by the plain and rotation-lease paths. Secret containment preserved — the adapter still only
names the pool. Unblocks D&B Direct+'s auth. Test `TestAuthInjector_OAuth2CC` (token exchanged once,
reused across two data calls, Bearer injected). `go build ./...` + `go test ./...` green.

### 2026-07-07 — Task #8 Phase 1: per-adapter CallPolicy (ADR-0024)
Opened the async/multi-credential egress enhancement with **ADR-0024** (full design: async
submit→poll, match→fetch, oauth2-cc token exchange, path/multi-header creds — phased). Landed
**Phase 1 — per-adapter `CallPolicy`**: new `provider.PolicyOverrider` interface; `HTTPAdapter`
gains an optional `Policy *CallPolicy` field (nil = engine default, so all 56 existing adapters are
unchanged); the engine selects the budget per adapter via `policyFor` at the G3 Call site. G3 stays
in force — the override is still a hard timeout + breaker + capped retry; only the bound changes.
This unblocks the async wave (a slow provider can declare e.g. `{Timeout: 90s, MaxAttempts: 1}` and
poll internally). Tests: `TestPolicyOverride_AsyncBudget` (override wins over the engine default),
`TestPolicyOverride_ZeroKeepsDefault` (unset Policy = no override). `go build ./...` +
`go test ./...` green. Phases 2–4 (oauth2-cc injection, AsyncHTTPAdapter, path/multi-header) scoped
in the ADR for subsequent iterations.

### 2026-07-07 — 200-provider rollout, Wave 6 complete — 56 adapters live
Added **ninjapear** (L6 firmo, Bearer, Nubela public-web aggregation, DEPRIORITIZED), **pipl** (L1
identity, `key` query, identity graph, DEPRIORITIZED), **versium** (L1 identity, `x-versium-api-key`,
US B2B2C append, DEPRIORITIZED). **Wave 6 fully processed** (11 researched): 6 implemented, 1
EXCLUDED (Sales.Rocks — no self-serve API), 4 deferred (D&B oauth2-cc+match→fetch, Explorium
match→enrich, Endole search→fetch+Basic → task #8; MixRank path-segment API key incompatible with
egress). **56 real adapters** now span L1/L2/L3/L4/L5/L6/L7/L8 — the cleanly-implementable
synchronous single-shot provider set is complete. `go build ./...` + `go test ./...` green.

### 2026-07-07 — 200-provider rollout, Wave 6 (L6 firmo long tail) — 53 adapters live
Added **data-axle** (`X-AUTH-TOKEN`, US/CA compiled Places match), **owler** (`user_key` header,
crowdsourced firmo, DEPRIORITIZED), **leadspace** (Bearer, AI-graph firmo + technographics,
DEPRIORITIZED). Wave-6 deferred: **D&B** (oauth2-cc + match→data-block) and **Explorium**
(match→enrich) → task #8; **MixRank** deferred — its API key is a mandatory URL **path segment**,
incompatible with the header/query-only egress key-injector under secret containment. Still
researching: pipl, versium, ninjapear, sales-rocks, endole. `go build ./...` + `go test ./...` green.

### 2026-07-07 — 200-provider rollout, Wave 5 complete — 50 adapters live
Added **vainu** (L6 firmographics — `API-Key` header, registry-backed Nordics/EU firmo + tech) and
**global-database** (L6 firmographics — `Authorization: Token <key>`, official company-registry
firmo + SIC). Both were verified against their real official docs (developers.vainu.com,
api.globaldatabase.com/docs/v2) despite their research agents running with the safety classifier
unavailable — citations checked, no fabrication (UNVERIFIED items flagged). **Wave 5 fully
processed** (15 researched): 8 implemented, 2 EXCLUDED (Nimbler, Swordfish — no public API), 5
deferred (Lead411 JWT-session, Wiza/SignalHire async, Enlyft solution_id-config, InfobelPro async).
**50 real adapters** now span L1/L2/L3/L4/L5/L6/L7/L8. `go build ./...` + `go test ./...` green.

### 2026-07-07 — 200-provider rollout, Wave 5 cont. — 48 adapters live
Added **contactout** (L2 email-find, DEPRIORITIZED — `token` header, per-address email_status map),
**diffbot** (L6 firmographics — KG Enhance `type=Organization`, `token` query, foundingDate→year),
**hg-insights** (L7 technographics — Bearer, install-base products + firmographics). Wave-5 research
completed 15/15. Additional triage: **Wiza + SignalHire deferred** (async), **Enlyft deferred**
(mandatory `solution_id` account-config query param + unverified envelope). Remaining to verify:
infobelpro, vainu, global-database. `go build ./...` + `go test ./...` green.

### 2026-07-07 — 200-provider rollout, Wave 5 (L4 phone-find + contact finders) — 45 adapters live
Implemented 3 by-identity contact/phone providers: **salesintel** (L4 — `X-CB-ApiKey`, human-verified
contacts + phones by type mobile/direct/office, ACTIVE-CANDIDATE), **lusha** + **kaspr** (L2 —
single-shot contact finders, DEPRIORITIZED LinkedIn provenance; Kaspr needs a raw-`Authorization` +
`accept-version: v2.0` header pair). Wave-5 triage: **EXCLUDED** — Nimbler & Swordfish (no
public/self-serve REST API — access is account-gated with no discoverable endpoint/schema).
**Deferred** — Lead411 (two-step JWT session auth the egress model doesn't do + fully-undocumented
response schema). `go build ./...` + `go test ./...` green. Still researching: contactout, wiza,
signalhire, hg-insights, enlyft, diffbot, infobelpro, vainu, global-database.

### 2026-07-07 — 200-provider rollout, Wave 4 (L8 intent) — 42 adapters live
Implemented **6sense** (L8 intent — the one Wave-4 provider keying on a canonical identity:
form-urlencoded POST, `Authorization: Token <token>`, returns intent_score + buying stage + segment
topics + firmographics). Wave-4 triage (honest, ADR-0009): **EXCLUDED** — TechTarget (no REST enrich
API; CRM/SFTP delivery only) and Cargo (orchestration platform, no field-returning endpoint).
**Deferred — visitor-ID/IP flow not modeled** — Albacross, Clearbit Reveal (input is a visitor IPv4;
no canonical `ip` Field), Leadfeeder (account visitor feed, not by-domain). **Deferred — async/OAuth
(task #8)** — Bombora (submit→poll CSV), Demandbase (oauth2-cc+async), BetterContact, Cleanlist.
**Deferred — schema unverified** — TrustRadius (API confirmed but response JSON schema only inferable
from JS-SPA docs; not shipping guessed field paths). `go build ./...` + `go test ./...` green.

### 2026-07-07 — Verification hardening: extended-vocabulary engine integration test
Added `TestNewAdapters_EngineIntegration` driving clearbit (firmographics incl. the multi-valued
`technographics`) + zerobounce (email_status) through the full Router→Engine→Store spine — proving
the ADR-0023 canonical Fields survive Field.Valid() + the G5 provenance store and the router selects
the right provider per wanted Field. Complements the existing hunter+twilio spine test. Wave-4
research (intent/orchestration) in flight; the async/multi-credential set (task #8) remains deferred
pending the engine per-call-timeout design decision. `go build ./...` + `go test ./...` green.

### 2026-07-07 — 200-provider rollout, Wave 3 complete — 41 adapters live
Added the last 2 single-shot Wave-3 providers: **fullcontact** (L1 identity, Bearer POST
company.enrich, DEPRIORITIZED) and **storeleads** (L6 firmographics, Bearer, e-commerce firmo +
technographics; revenue cents→dollars). Wave-3 verdicts finalized: **UserGems EXCLUDED** (write-only
ingestion API — no enrich response, ADR-0009), **PredictLeads deferred** (two distinct auth headers
`X-Api-Key`+`X-Api-Token` — egress injects one credential/descriptor; egress-seam enhancement),
**RocketReach deferred** (async lookup). `go build ./...` + `go test ./...` green. Next: L8 intent
(Bombora, 6sense, Demandbase, TechTarget, TrustRadius, PredictLeads-events), L9 orchestration
(BetterContact, Cleanlist, Cargo), L4 phone-find, and the deferred async/multi-cred set (task #8).

### 2026-07-07 — 200-provider rollout, Wave 3 (L6 firmographics + L1 identity) — 39 adapters live
Implemented 5 firmographics/identity providers from the Wave-3 research: **crunchbase** (POST
search-by-website_url so a domain enriches in one call, `X-cb-user-key`), **opencorporates**
(official registry search, `api_token` query), **ocean-io** (`X-Api-Token`, POST enrich, funding +
tech), **the-companies-api** (`Authorization: Basic <raw-token>`, full firmo + naics/sic + tech),
**coresignal** (`apikey` header, DEPRIORITIZED — LinkedIn-derived). Added `yearOf` (ISO→year) and
`bareDomain` (URL→domain) helpers. Each docs-cited, fixtured, decode-tested, registered.
`go build ./...` + `go test ./...` green. Remaining Wave-3 (still researching): predictleads,
fullcontact, storeleads, usergems, rocketreach. Next: L8 intent (Bombora, 6sense, Demandbase,
TechTarget…), L9 orchestration (BetterContact, Cleanlist, Cargo), L4 phone-find.

### 2026-07-07 — 200-provider rollout, Wave 2 L5 complete — 34 adapters live
Implemented the 5 previously session-limited phone validators (researched directly): **numverify**
(`access_key` query, 200-`success:false` classified), **abstract-phone** (`api_key` query,
`type`+`format.international`), **veriphone** (Bearer, phone_valid+phone_type), **byteplant-phone**
(`APIKey` query, `status`/`linetype` with API_KEY/RATE/DELAYED classified), **telesign** (Basic
`customerid:apikey`, phone_type.description). **L5 phone-validation now covers 12 providers** (all
but Sinch, which needs a `{projectId}` path config). Added `fixed-line` to `phoneStatusFromType`.
`go build ./...` + `go test ./...` green. Remaining: L4 phone-find (mostly DEPRIORITIZED), L8 intent,
L9 orchestration, L1/L6 remainder, and the deferred async set (task #8).

### 2026-07-07 — 200-provider rollout, Wave 2 (L5 phone validation) — 29 adapters live
Implemented 6 of 7 ready phone-validation providers from the Wave-2 research: **telnyx** (Bearer,
carrier.type), **vonage** (Basic `key:secret`, `network_type` gated by `status` int → AUTH/QUOTA/
RATE_LIMIT classified), **messagebird** (`Authorization: AccessKey <key>`, `type`), **ipqualityscore**
(`IPQS-KEY` header, `valid`+`line_type`, 200-`success:false` classified), **plivo** (Basic
`authid:token`, carrier.type), **infobip** (`Authorization: App <key>`, HLR status/error →
valid/invalid/unreachable). All normalize to a single **phone_status** vocabulary
(valid_mobile|valid_landline|valid_voip|valid_other|valid_unknown|invalid|unreachable|unknown) via a
new shared `phoneStatusFromType` helper; carrier/line-type adapters echo the normalized E.164 back to
`mobile_phone`. Providers whose auth needs a header prefix (MessageBird `AccessKey `, Infobip `App `)
store the secret WITH the prefix (like Twilio/Mailgun composite secrets). **Sinch deferred** (mandatory
`{projectId}` path config, no account-agnostic variant). **5 providers pending research** — telesign,
abstract-phone, numverify, byteplant-phone, veriphone — hit a session limit mid-workflow; will
re-research when it resets. `go build ./...` + `go test ./...` green.

### 2026-07-07 — 200-provider rollout, L6/L7 fill — 23 adapters live
Added **wappalyzer** (L7 technographics — `x-api-key`, top-level-array response, frontend tech
stack) and **brandfetch** (L6 firmographics — Bearer, `GET /v2/brands/{domain}`: company_name,
employees, founded year, industry, `kind`→company_type, HQ city/country, LinkedIn from links[]).
Both researched from official docs (cited docs/03 §7), single-shot, fixtures + decode tests +
registry entries. Diffbot deferred (Knowledge-Graph entity schema needs a live sample to map
reliably). `go build ./...` + `go test ./...` green. Wave-2 phone-validation research in flight.

### 2026-07-07 — 200-provider rollout, Wave 1 (L2 email-find + L3 verify) — 21 adapters live
Completed the Wave-1 research (13/13 specs, 0 errors) and implemented all **9 single-shot** providers
from it:
- L2 email-find: **findymail** (Bearer), **anymailfinder** (raw-key `Authorization`), **datagma**
  (`apiId` query — work_email + email_status + company_domain).
- L3 email-verify: **emailable** (`state`), **bouncer** (`x-api-key`, `status`), **mailgun-validate**
  (Basic `api:key`, `result`), **millionverifier** (`result`), **debounce** (`debounce.result`),
  **clearout** (Bearer POST, `data.status`).

Added `423 Locked → QUOTA` to the shared `classifyStatus` (Findymail paused-subscription). Added a
shared **`classifyErrMsg`** helper that maps a vendor's in-body error message to AUTH/QUOTA/RATE_LIMIT
— used by MillionVerifier, DeBounce, and Clearout, which all return bad-key/out-of-credits as HTTP
200 with an error field (now correctly failed-over via the `HTTPAdapter` classified-error path,
proven by an expanded `TestWave0_InBodyErrorClassified` table). Deferred as async/OAuth multi-step
(researched, not coded): icypeas, enrow (submit→poll), snov (oauth2-cc), verifalia (submit→poll) —
joining dropcontact/cognism/fullenrich under the async-adapter enhancement (task #8). `go build ./...`
+ `go test ./...` green.

### 2026-07-06 — 200-provider rollout, Phase B (adapters, wave-by-wave) — in progress
**12 real adapters** now on the ADR-0023 bridge, each researched from official docs (cited in
`docs/03 §7`), secret-free on the `hunter.go` pattern, with a pinned `UNVERIFIED` fixture +
table-driven decode test + registry entry:
- L1: **people-data-labs** (`X-Api-Key`, likelihood-derived confidence).
- L2: **hunter**, **prospeo**, **apollo** (DEPRIORITIZED — LinkedIn/web provenance; work_email conf lifted when `email_status==verified`).
- L3: **neverbounce**, **kickbox** (conf from `sendex`), **zerobounce**.
- L5: **twilio-lookup**.
- L6: **clearbit** (firmographics — name/industry/sic/naics/employees/revenue/tech/geo/founded/type/linkedin).
- L7: **builtwith**, **theirstack** (technographics; job-posting-derived for TheirStack).
- L8: **g2** (buyer intent — buying_signal, intent_topics, buyer-org firmographics).

Wave-0 research workflow completed 11/11 specs (0 errors) from official docs. Added a general
**`HTTPAdapter` enhancement**: a `Decode` that returns a classified `*domain.ProviderError` is now
preserved (not flattened to BAD_REQUEST), so the widespread **200-with-in-body-error** pattern
(ZeroBounce/BuiltWith bad-key/out-of-credits) maps correctly to AUTH/QUOTA for key failover. New
`adapters.normList` normalizes multi-valued technographics/intent into one sorted comma-joined value
(ADR-0023). **Deferred** (need an async/redeem-capable adapter): dropcontact, cognism, fullenrich
(two-step flows) — researched, not shipped as fabricated single-call adapters. `go build ./...` +
`go test ./...` green throughout.

### 2026-07-06 — 200-provider rollout, Phase A (groundwork bridge) — ADR-0023
Built the bridge that makes real API-first adapters runnable at scale, ahead of the per-provider
waves (`Closo_Enrichment_Architecture_200_Tools`). **Field vocabulary** extended doc-first
(`docs/00 §7` then `internal/domain/field.go`, kept in lockstep): code caught up to the Glossary
(`naics`, `sic`, `technographics`, `intent_topics`, `funding_stage`) and added the L6–L8 firmo/intent
Fields (`company_revenue`, `company_founded_year`, `company_hq_country`, `company_hq_city`,
`company_type`, `company_linkedin_url`, `company_phone`, `duns_number`, `intent_score`,
`buying_signal`) — 18→33 canonical Fields, additive, no migration (`technographics`/`intent_topics`
stored as a single normalized comma-joined value). **Adapter registry**
(`internal/provider/adapters/registry.go`): append-only single source of truth; `All(client)` builds
the engine slice, `Hosts()` builds the egress allow-list; `TestRegistry_Invariants` enforces
Slug==NameV, `<slug>:default` selector prefix, canonical capability Fields, and https base URLs
(also fixed a latent `twilio-lookup` slug/selector mismatch). **Catalog seeder**
(`cmd/providerseed` + in-package `providers.Seed`): UPSERTs one `providers` row per registered
adapter from its introspected descriptor under `PlatformTx`; new rows land `op_state='disabled'`,
re-seeds refresh only the integration descriptor (operator lifecycle state preserved) — proven by
`seed_test.go`. **Binaries:** `cmd/enrichapi` now wires `adapters.All(egress)` through
`provider.NewEgressClient` with keys from `PROVIDER_KEYS` (or the rotation lease resolver in the
full platform); `cmd/enrichd` stays an offline demo but enumerates the registry. `go build ./...`
and `go test ./...` green.

### 2026-07-06 — Dashboard pending-OI closeout (post-P12 hardening waves)
Closed the open-items backlog after the P0–P12 build. Migration `0011` (mfa_used_steps,
dash_admin_idempotency, alert_rules.anomaly_floor_credits). **Security:** TOTP single-use replay
guard (VerifyAndConsume, login + step-up); durable admin idempotency ledger (replaces the in-process
map); fingerprint-pepper rotation; NIST SP800-38D AES-256-GCM KATs + PBKDF2-HMAC-SHA256 KATs;
X-Forwarded-For-spoof + session-fixation negatives; bulk session-revoke. **Telemetry:** live
rotation `Lease.Done` → usage_events feed (Config.RecordUsage). **Bulk jobs:** keys bulk-op/import on
the durable bulk_jobs lease model + an advisory-locked janitor that fails expired-lease jobs.
**Cost/alerts:** cost.anomaly added to the closed metric catalog + /meta/enums; per-rule anomaly
floor. **enrichd:** opt-in worker heartbeat with a minted HS256 machine JWT. **Contracts/tooling:**
openapi-admin.{json,yaml} + apispec parity test (145==145); pgmigrate `-- pgmigrate: no-transaction`
escape hatch; web `check:ci`. **Resilience:** configver test-only publish-crash fault hook +
PG-restart-reconnect + poison-import-row chaos tests; 50k-import and 1M-fold measured single-instance.
**Live E2E:** Playwright login→MFA→overview passes end-to-end — caught and fixed a real SPA
history-fallback bug (deep links / refresh 404'd). **Repo integrity:** fixed a `.gitignore` rule
(`secrets/`) that had gitignored the entire internal/dash/secrets envelope-encryption package since
P0, so the committed tree now builds from a clean checkout. Design-target stores
(Redis/ClickHouse/Kafka/Temporal) + WORM anchor recorded as deploy-time decisions. Residuals to
staging: full-scale multi-instance/10-min load, enrichd drain-gating (OI-P5-2), bulk auto-resume
(OI-KEYS-1c), recovery-code-on-step-up.

### 2026-07-06 — Waterfall Management Dashboard build (P0–P12) — control-plane + 12 module UIs + P12 hardening closure
Delivered the full admin dashboard for the enrichment engine across twelve one-commit phases on branch
`waterfall` (contract: `docs/waterfall-dashboard/12`). **Backend** (`internal/dash/*`, 21 packages, stdlib-only):
P0 identity/tenancy/session/audit spine (dual-GUC RLS `db`, `httpx` auth+CSRF+idempotency chain, `rbac`,
`security` pbkdf2+RFC-6238 TOTP, hash-chained `audit`, AES-256-GCM `secrets`) + `cmd/dashboardd` (env→pool→
migrations→routes→`/healthz` `/readyz` `/metrics`); P1 providers catalog + keys/pools + envelope-sealed 1k
CSV import; P2 rotation engine (12 strategies, batched quota leases, KM-3 trigger machine); P3 config
versioning + routing/waterfall validators + zero-egress dry-run; P4 telemetry backbone (usage_events + all
rollups) + provider health center + approvals quorum engine + leader-elected loops; P5 queues/workers read
model over `job_outbox` + pgoutbox redrive + heartbeat; P6 cost analytics + alerts evaluator/notifier
(SSRF-guarded); P7 overview 2s aggregator + multiplexed SSE realtime + Last-Event-ID replay. Migrations
0004–0010 (append-only, FORCE RLS on every table). **Frontend** (`web/`, Vite+React+TS, ADR-0016 locked deps):
P8 design system + typed api client + SSE manager + auth; P9 providers/keys(1k virtualized grid)/rotation/
health; P10 routing(dnd-kit)/workflows/queues/dead-letters/workers; P11 cost/alerts/security/approvals/settings
+ a11y. **P12 hardening (2026-07-06):** converted the runnable single-instance UNVERIFIED targets to measured
numbers in doc 13 §6 — L1 key-selection **24.7M sel/s** @ -cpu=8 (0 allocs, ~2,470× the 10k/s target;
`BenchmarkPoolSelect` + no-over-lease `TestRotationLeaseNoOverLease`), L2 SSE 200-client/20s soak **p99 12.27ms**
(≤2s), zero dropped changed events (`TestSSESoakLite`), L3 1k-key import sealed zero-plaintext, L4 100k-event
fold→refold **byte-identical** across 9 rollup tables; web bundle **111.2 KB gz** initial (budget 400 KB).
**Live boot smoke passed**: dashboardd booted against an ephemeral PG17 with bootstrap (10 migrations + `dash_app`
role provisioning), served the SPA + liveness/readiness/metrics, rejected the unauthenticated admin route (401),
completed a pbkdf2 login (operator→`mfa_required`, tenant_user→`ok`+csrf), and served six authenticated operator
reads (audit-verify `{ok:true}`, queues, dead-letters, overview, workers, audit-log) all 200; clean SIGTERM
shutdown. **Security pass:** secret scan clean (only synthetic test placeholders); RLS zero-rows release blocker +
fuzz + G2 replay + CSRF/idempotency/SSRF-notifier/formula-injection suites green via `scripts/run-rls-test.sh` on
PG17.10. **Chaos (covered subset):** aggregator-leader failover (`TestOverviewAggregatorFailover`,
`TestTelemetryLeaderElection`) + publish-crash consistency (`TestConcurrentPublishConflict`) satisfy their §7
invariants; PG-restart-reconnection + poison-import-row + publish-crash fault-injection deferred to staging.
**Runbook validation:** RB-5/6/7/12 Diagnosis/Verification read commands executed live against the booted
dashboardd (all 200). Gates: `go build ./... && go vet ./...` clean (47 packages); web `tsc --noEmit` + 192
vitest + no-orphan-UI + build green. Docs `waterfall-dashboard/00–14` flipped DRAFT→ACCEPTED; doc 00 §8 UNVERIFIED
register + doc 13 §6 load table updated with measured values; doc 12 §5 Self-Verification Record refreshed with
P12 measured evidence + closure line. **Honestly deferred (OI-P12-1..3):** full-scale/multi-instance load
(500-client/10-min SSE soak, 50k-row import, 1M-event fold, API p95 @ 200 rps), the remaining chaos drills +
RB-14 restore RPO/RTO, and the Playwright-against-live E2E run — all to a staging load-lab.

### 2026-07-01 — Implementation Slice 20 (Go) — config validation + startup self-check
Human approved making misconfiguration fail loudly at startup instead of per-request. New
`internal/config`: `Load(getenv)` (pure, unit-testable) validates PORT (1..65535), DSNs (must have
user=+dbname=), OUTBOX_MAX_ATTEMPTS (≥1), JWT_HS256_SECRET (≥16 bytes), and coherence (admin/relay
DSN require a primary DSN; POSTGRES_DSN and DURABLE_LOG are mutually exclusive), returning ALL
problems joined; `main` refactored to read the validated Config instead of scattered os.Getenv.
`cmd/enrichapi` `startupSelfCheck` (Postgres mode): refuses to start if the app connects as a role
that bypasses RLS (superuser/BYPASSRLS — would silently defeat G1) and if required tables are
absent. New primitives: `pg.Conn.RolePrivileges()` (super/bypassrls) and `pgmigrate.Pending()`
(migration drift). New `GET /readyz` (distinct from /healthz liveness) wired to `pgstore.Store.Ping`
— 200 only when the datastore is reachable. Live-verified (PG17): bad config logs all three errors
+ refuses to start; a superuser app DSN → refuses to start with the G1 message; memory-mode /readyz
→ ready; `TestRolePrivileges` + `TestPending_ReportsUnapplied` pass; the Slice-16 crash harness
still passes (40/40, happy-path self-check as app_rls). Unit: `config` (4) + `/readyz` (200/200/503).
OpenAPI declares /readyz. Mainline (99 tests) `go build/vet/test/gofmt` clean. New doc `docs/42`.
Continuous health, config-file loading, and relay/vendor readiness honestly deferred.

### 2026-07-01 — Implementation Slice 19 (Go) — consolidation: README, one-command demo, docs index
Human approved a consolidation pass to make the 18 slices approachable + runnable. Added a
top-level `README.md` (what it is, the five correctness gates G1–G5 + the "model proposes, gate
disposes" invariant, an architecture diagram, the stdlib-only property, a copy-pasteable
quickstart, the full unit/live/crash testing story, a repo map, and an explicit honest-deferrals
section — every claim backed by code or a test). Added `scripts/demo.sh`: one command, five phases
— build → unit suite → offline `enrichd` provenance demo → live HTTP round-trip against the gateway
in memory mode (real JSON + `/metrics`) → auto-detected live PostgreSQL harnesses (skipped
gracefully when PG17 is absent). Updated `docs/README.md` (replaced the stale "awaiting approval"
status with the real 18-slice state; indexed slices 23–40 + the top-level README). godoc audited
complete (no change needed). **Bugfix:** building the demo surfaced a real latent race in
`scripts/run-rls-test.sh` — five integration packages share one database but `go test` ran their
binaries in parallel, so `pgmigrate`'s drop/recreate intermittently raced `pgoutbox`'s setup;
fixed with `-p 1` (serialize). Re-verified: all 9 harness tests deterministic, and the
run-rls → crash-recovery chain tears down cleanly on the shared port. No Go source changed;
mainline (94 tests) unaffected. New doc `docs/41`.

### 2026-07-01 — Implementation Slice 18 (Go) — DLQ redrive / replay
Human approved closing the inspect-only-DLQ gap from Slice 17: an operator can now redrive a
parked job so the outbox re-delivers it after the bug is fixed. `Store.Redrive(ctx, jobID)` is one
RLS-scoped `UPDATE … WHERE job_id=$1 AND dead` that resets `dead=false, pending=true, attempts=0,
claimed_at=null, last_error=null, status='queued'` (payload untouched → same job re-runs, G2-safe)
and reports whether a dead row was reset. `POST /v1/dead-letters/{id}/redrive` is a write (gated on
the write scope, 403 without), tenant-scoped (G1), returns 404 when nothing dead matches, is
audit-logged (`dlq_redrive` with tenant+user+job) and counted (`dlq_redrive_total`); the
`DeadLetterLister` interface grew a `Redrive` method (now `DeadLetterAdmin`), wired via the same
decoupling adapter. Live-verified end-to-end (`TestPGOutbox_RedriveReplaysParkedJob`, PG17): park a
poison job → tenant-B redrive denied (RLS) → tenant-A redrive resets it and it leaves the DLQ → a
now-working worker re-delivers and completes it (`succeeded`, work_email filled) → a second redrive
of the completed job is a no-op. Writing the test caught the Slice-17 slow-job-vs-visibility hazard
(a 1ms visibility re-dead-lettered the in-flight job); fixed operationally (visibility > worker
time). OpenAPI declares the route (200/401/403/404). Mainline (94 tests) `go build/vet/test/gofmt`
clean. New doc `docs/40`. Bulk/auto/cross-tenant redrive honestly deferred.

### 2026-07-01 — Implementation Slice 17 (Go) — outbox dead-letter queue + max-attempts
Human approved closing the reliability gap flagged across Slices 13/16: the at-least-once outbox
redelivered a failing job forever. The gap is specifically the CRASH LOOP — a job that RUNS and
errors is already terminal (`failed`) and not redelivered; a job whose worker dies before any
terminal `Put` stays pending and loops. Migration `0003_outbox_dlq.sql` adds `attempts`/`dead`/
`last_error` + a partial dead index. `Relay.claim` (rewritten) increments `attempts` inside the
same atomic `UPDATE … FOR UPDATE SKIP LOCKED`; a claim that would exceed `maxAttempts` parks the
row (`dead=true, pending=false, last_error=…`) instead of delivering, and parked rows are never
scanned again. New `NewRelay` options `WithMaxAttempts(n)` (default 10) + `WithDeadLetterHook(fn)`;
tenant-scoped `Store.DeadLetters(ctx, limit)` + `GET /v1/dead-letters` (registered only when a
lister is wired). `cmd/enrichapi` wires `OUTBOX_MAX_ATTEMPTS`, the `outbox_dead_letter_total`
counter + a Warn log, and the DLQ endpoint via an adapter (keeps `api`/`pgoutbox` decoupled).
Live-verified (`TestPGOutbox_DeadLetterAfterMaxAttempts`, PG17): after 3 deliveries the next
claim parks the poison job, the hook fires exactly once, the tenant-scoped DLQ read returns it
(attempts>max, last_error set), further drains don't re-claim it, and tenant-B sees none (G1). The
Slice-16 crash-recovery harness still passes unchanged (2 pending at crash → 40/40 recovered, 40
ledger rows). Mainline (94 tests) `go build/vet/test/gofmt` clean. New doc `docs/39`. Redrive/replay,
slow-job-vs-visibility tuning, and alert routing honestly deferred.

### 2026-07-01 — Implementation Slice 16 (Go) — wire the full Postgres durable path into the binary
Human approved wiring everything built for Postgres over Slices 10–14 (RLS store, G2/G4 ledgers,
transactional outbox, migration runner) into `cmd/enrichapi` and proving it end-to-end through the
real binary. Datastore selection is now three-way, most-durable-first: `POSTGRES_DSN` → `pgstore`
engine/record store (RLS) + `pgoutbox` job store/submitter + a privileged `pgoutbox.Relay`
(FOR UPDATE SKIP LOCKED, 3s visibility) that recovers in-flight jobs after a crash; `DURABLE_LOG`
→ file-WAL; neither → in-process. When `POSTGRES_ADMIN_DSN` is set, startup runs the migration
runner and idempotently provisions two NON-superuser roles — `app_rls` (RLS-enforced) and `relay`
(BYPASSRLS, claim only) — so a fresh cluster comes up ready yet tenant isolation (G1) is enforced
at runtime (the app is not a superuser/owner and cannot bypass RLS). New
`scripts/crash-recovery-test.sh` drives the real compiled binary against an ephemeral PG17
cluster: submit 40 async jobs → `kill -9` (a crash) → restart → assert all complete. Observed:
40 durably captured, **3 still pending at the kill**, **40/40 records recovered**, 40 outbox rows
delivered, **40 idempotency-ledger rows for 40 jobs (G2: no double execution on redelivery)**,
0 pending — **PASS**. Mainline (94 tests) `go build/vet/test/gofmt` clean. New doc `docs/38`.
Trust/superuser bootstrap, shell-harness (not go test), single-relay, and DLQ honestly deferred.

### 2026-07-01 — Implementation Slice 15 (Go) — real-provider HTTP smoke + pinned fixtures
Human approved exercising the real vendor adapters (Hunter/Prospeo/Twilio) end-to-end through the
egress key-injection seam against mock vendor servers, and pinning the assumed response shapes as
checked-in fixtures to narrow the no-fabrication gap on vendor wire formats. Added
`testdata/{hunter_found,hunter_empty,prospeo_found,twilio_found}.json` + `README_UNVERIFIED.md`
(states the `UNVERIFIED` marker + the exact promotion path: sandbox key → capture raw 2xx →
reconcile Decode → record source_url/verified_date). New `live_smoke_test.go`:
`TestAdapters_DecodeRecordedFixtures` (each adapter decodes its pinned fixture through the real
`AuthInjector`; empty Hunter data → no observation, not an error), `TestAdapter_EgressSSRFBlocked`
(a real adapter through `NewEgressClient` to an http/loopback host is refused before connecting —
`ErrSSRFBlocked` → non-retryable BAD_REQUEST — the SSRF choke is live on the adapter path), and
`TestAdapters_StatusErrorMatrix` (401→AUTH, 402→QUOTA, 403→RATE_LIMIT, **404→NOT_FOUND**,
429→RATE_LIMIT, 400→BAD_REQUEST, 500→TRANSIENT, 503→PROVIDER_DOWN). VERIFIED: auth scheme +
injection and status→error-class mapping. Still UNVERIFIED (honestly): the JSON field names —
now a single tested, labelled place. Mainline (94 tests) `go build/vet/test/gofmt` clean. New doc
`docs/37`. No live vendor was called (requires an authorized key + approval).

### 2026-07-01 — Implementation Slice 14 (Go) — SCRAM-SHA-256 auth + TLS + migration runner
Human approved hardening the stdlib `pg` client for real deployments (still zero external deps).
**SCRAM-SHA-256** (RFC 5802/7677, no channel binding) implemented in `pg/scram.go` — PBKDF2 via
Go 1.24 `crypto/pbkdf2`, client-proof = ClientKey XOR HMAC(StoredKey, AuthMessage), and mutual
auth (the server-final verifier is checked constant-time); wired into the startup handshake as
SASL (auth code 10). **TLS**: the `SSLRequest` negotiation + `crypto/tls` upgrade, exposed via
`Config.TLS` and DSN `sslmode` (libpq semantics: require / verify-ca / verify-full). **Migration
runner** (`internal/pgmigrate`): applies `NNNN_*.sql` in order into a `schema_migrations` table,
each file + its version row in one transaction (atomic, idempotent); migrations 0001/0002 had
their `BEGIN/COMMIT` removed so the runner owns the transaction. Verified: `TestSCRAM_RFC7677Vector`
(mainline — exact client proof + server verifier against the RFC worked example),
`TestConn_SCRAM` (live — real scram password role), `TestConn_TLS` (live — `pg_stat_ssl` confirms
the backend is encrypted), `TestApply_OrderedAndIdempotent` (live — ordered, no-op re-apply).
**9 live integration tests** now pass on PostgreSQL 17.10; mainline (91 tests) clean. New doc
`docs/36`. Channel binding (SCRAM-PLUS), MD5, cert-verify-by-default, and down-migrations honestly
deferred.

### 2026-07-01 — Implementation Slice 13 (Go) — Postgres transactional-outbox durable queue
Human approved replacing the file-WAL durable queue (Slice 03) with a Postgres transactional
outbox. New migration `0002_job_outbox.sql`: one `job_outbox` row per job (payload jsonb +
`pending` intent + `claimed_at`), RLS + FORCE, partial index over pending rows. `pgoutbox.Store`
(drop-in `job.Store` + `job.Submitter`) mirrors the file-WAL semantics: `Submit` durably captures
the job (`INSERT ... ON CONFLICT DO NOTHING`, never sheds), `Put` clears `pending` iff terminal in
the same UPDATE as the snapshot, `Get` is RLS-scoped — all tenant-GUC-bound (G1;
`ErrTenantMismatch` on a cross-tenant submit). `pgoutbox.Relay` claims pending rows with `FOR
UPDATE SKIP LOCKED` (competing consumers — multiple replicas poll without double-claiming) and a
visibility timeout that recovers a crashed relay's in-flight claims. Live-verified on PostgreSQL
17.10 (`TestPGOutbox_DurableDeliveryAndCrashSafety`): normal delivery (provider once; outcome
round-trips through JSONB; completed job not re-claimed); **crash + redelivery** (reset row to
pending → re-claimed → re-run → **0 new provider calls**, G2 exactly-once-effective);
visibility-timeout (recent claim skipped, stale claim recovered); tenant isolation on reads.
Mainline `go build/vet/test/gofmt` clean; **6 live integration tests** now pass. New doc `docs/35`.
Not wired into cmd (needs an app role + a BYPASSRLS relay role provisioned by ops); same-txn event
outbox and DLQ/max-attempts honestly deferred.

### 2026-07-01 — Implementation Slice 12 (Go) — Postgres G2/G4 ledgers + connection pool
Human approved porting the last two ledgers to Postgres so EVERY correctness gate is enforced
at the datastore with RLS, not just G5. `pgstore` is now a full `store.Store`. **G2**
(idempotency): `Record` uses `INSERT ... ON CONFLICT DO NOTHING` (first-writer-wins;
`provider.Result` stored as jsonb), `Lookup` JSON-decodes it back — RLS-scoped so a tenant can
neither read nor forge another's entry. **G4** (cost): the reservation is a single guarded
`UPDATE ... WHERE committed + amount <= ceiling RETURNING committed` — zero rows ⇒
`ErrCeilingExceeded` with no change; a row lock serializes concurrent reservations so the
ceiling holds under contention; `Release` refunds via `GREATEST(0, ...)`. Added `internal/pg.Pool`,
a bounded connection pool (token-capped open conns; reuse; broken-eviction) so each op checks
out a connection, runs one transaction that binds the tenant GUC `SET LOCAL`, and returns it —
never sharing a connection across tenants mid-transaction. The full-stack E2E now uses `pgstore`
as the ENTIRE store (G2/G4/G5 all datastore-durable) and additionally asserts the ledger tables
are non-empty post-run. New tests: `TestPool_BoundsAndReuse` (mainline, injectable dialer),
`TestPG_IdempotencyLedger` + `TestPG_CostLedger` (live: round-trip, first-writer-wins,
ceiling-rejection-leaves-state, tenant isolation on both ledgers). **5 live integration tests
pass on PostgreSQL 17.10**; mainline (89 tests) `go build/vet/test/gofmt` clean. New doc
`docs/34`. **⭐ All five gates now datastore-enforced with RLS + live-verified.** SCRAM/TLS,
migration runner, pool liveness checks, and a Postgres transactional outbox honestly deferred.

### 2026-07-01 — Implementation Slice 11 (Go) — full-stack end-to-end test (live)
Human approved a black-box, full-stack integration test proving the wired system upholds the
gates end-to-end. `internal/e2e` drives a real signed **JWT → HTTP gateway → async queue +
worker pool → Execution Engine → live PostgreSQL (FORCE RLS) → HMAC-signed webhook**; only the
vendor providers are deterministic fakes (they count calls for the G2 assertion), everything
between the JWT and the database is production code. Asserted over HTTP against a live cluster:
**G1** — a second tenant's `GET /v1/records` returns 0 fields (isolation enforced by the
database, not app code); **G2** — a second job for the same record+field+params triggers 0 new
provider calls (served from the idempotency ledger); **G4** — a `cost_ceiling:2` job against a
6-credit provider commits ≤ 2 (no overspend); **G5** — the value read back from Postgres carries
full provenance; plus a signature-valid, tenant-bound webhook delivered on completion. All pass
live in ~0.18s. Composite store binds G5→Postgres, G2/G4→memory (PG port later). The webhook
egress guard is bypassed in this test only (loopback sink; SSRF is unit-tested in Slice 05).
Added to `scripts/run-rls-test.sh`; mainline `go build/vet/test/gofmt` clean. New doc `docs/33`.

### 2026-07-01 — Implementation Slice 10 (Go) — Postgres store + live tenant-isolation (RLS) proof
Human approved closing the biggest prototype→production gap: gate G1 enforced by the DATABASE
via row-level security, proven live. To preserve the zero-external-dependency property, added
`internal/pg` — a stdlib PostgreSQL wire-protocol (v3) client: startup (trust/cleartext),
simple + extended (Parse/Bind/Execute/Sync) query protocols with **bound parameters** (no SQL
injection), text decoding with NULLs, structured `PGError`, and post-error `Sync` recovery.
Added `internal/pgstore` — a `store.FieldVersions` (G5) implementation whose every op runs in a
transaction binding `SET LOCAL app.current_tenant` from the request **principal** (never an
argument), with `Append` stamping `tenant_id = current_setting(...)` so the RLS `WITH CHECK`
confines writes to the caller's partition; fails closed with no principal. The migration
(`0001_init.sql`, `FORCE RLS` + `USING`/`WITH CHECK`) was applied against a **real PostgreSQL
17.10** and the docs/21 §1 release-blocker test **passed live**: run as a NON-superuser role
(superusers bypass RLS), cross-tenant read returns **0 rows**, `WITH CHECK` rejects a
cross-tenant INSERT, the app store isolates by principal, and an unauthenticated context is
rejected. Reproducible via `scripts/run-rls-test.sh` (ephemeral trust cluster or
`WATERFALL_PG_DSN`). Integration tests are `-tags integration` + DSN-gated; mainline
`go build/vet/test/gofmt` stays clean. New doc `docs/32`. **⭐ G1 datastore release-blocker
satisfied + live-verified.** G2/G4 Postgres ledgers, connection pooling, in-client SCRAM/TLS,
and a migration runner honestly deferred.

### 2026-07-01 — Implementation Slice 09 (Go) — real JWT auth (verified signed tokens)
Human approved replacing the static dev-token stand-in with real JWT verification (RFC
7519/7515), so the tenant principal driving G1 is now a cryptographically verified claim.
`internal/auth`: stdlib-only verifier (HS256 + RS256) with **`kid` rotation** and the
hardening a JWT verifier lives by — **the alg is pinned by the key, not the token header**
(defeating `alg:none` and the RS256→HS256 confusion attack), constant-time HMAC compare, `exp`
required + `nbf`/`iss`/`aud` validated with bounded clock leeway, and **`tenant_id` required &
non-empty** so G1 can never fall back to an ambient tenant. Signing lives only in a test-support
package (`authtest`); the production package verifies, never signs. `api.JWTAuthenticator` slots
into the existing `Authenticator` seam (gateway otherwise unchanged); a new optional
`Server.WriteScope` gives **scope-based authz** — a verified-but-unauthorized token is **403**,
distinct from 401, and any verification failure is 401 with no leak of which check failed.
`tenant.Principal` gained `Scopes`/`HasScope`. `cmd/enrichapi` enables JWT when
`JWT_HS256_SECRET` is set (else warns + falls back to dev tokens). 6 new tests (88 total): valid
HS256/RS256+rotation, a rejection table (expired, nbf, wrong iss/aud, missing tenant, unknown
kid, tampered payload, alg:none, malformed, wrong secret, **alg-confusion**), array-audience,
leeway; plus end-to-end API auth+scope. `go build/vet/test/gofmt` clean. **Live-verified:**
JWT-enabled service with externally-minted HS256 tokens → 202/403/401 across the matrix. New doc
`docs/31`. JWKS discovery, RS256 PEM/mTLS, and token revocation honestly deferred.

### 2026-07-01 — Implementation Slice 08 (Go) — calibration + bandit routing (learned components)
Human approved adding the two *learned* pieces of the methodology under the invariant "model
proposes, deterministic gate disposes". `internal/calibrate`: isotonic regression via PAVA — a
monotonic, opt-in, offline-fitted `raw score → P(correct)` map per `(provider, field)`, applied
**before** fusion (the fuse/SPRT now operate on calibrated confidence) while **provenance keeps
the raw provider score** (G5 intact). `internal/bandit`: dependency-free Beta-posterior Thompson
sampler (Marsaglia-Tsang Gamma→Beta) with a **conservative floor** (blend toward the static prior
until enough pulls) and a **seed-reproducible** per-plan scorer. New `router.Scorer` seam
(`WithScorer`) orders the cascade by sampled score/cost; bandit satisfies it structurally (no
import cycle); default preserves exact static-prior behavior. Engine `WithCalibrator`/`WithBandit`
close the loop — the engine updates the bandit after *real* calls only (cache hits don't
double-count) and the gates (G1–G5) are untouched. Wired into `cmd/enrichapi` with a per-request
seeded scorer (race-free). 10 new tests (82 total): PAVA monotonicity + overconfidence
correction, opt-in/nil-identity, posterior shift, no-data⇒prior, replayable scoring, sample-range;
router reorder; **closed-loop learning over 40 records** (mean(good) > 0.6 > 0.5 > mean(bad)) and
calibration-reflected-in-resolved-value. `go build/vet/test/gofmt` clean. New doc `docs/30`.
Online calibration/label-feedback, contextual/cost-aware regret bounds, and durable/shared bandit
state honestly deferred.

### 2026-07-01 — Implementation Slice 07 (Go) — observability (metrics + structured logs)
Human approved the observability increment. Added `internal/metrics` — a dependency-free,
concurrency-safe Prometheus registry (labeled counters/gauges/gaugefuncs/histograms → text
exposition). Instrumented the API with **RED golden signals** (`http_requests_total`,
`http_request_duration_seconds`) + a `/metrics` endpoint + one structured `slog` line per request
using the **route template** (never the concrete path → no PII in labels/logs). Instrumented the
engine with provider health + **enrichment KPIs** (`provider_calls_total{provider,result}` incl.
`breaker_open`/`blocked`, `provider_call_duration_seconds`, `provider_cost_credits_total`,
`enrichment_fields_filled_total`). Added `queue_depth` GaugeFunc + `webhook_deliveries_total`.
7 new tests (72 total): registry rendering/escaping/re-register, `/metrics` RED with `{id}`
template + **no leaked id**, engine cost/fields metrics. `go build/vet/test/gofmt` clean.
**Live-verified:** scraped `/metrics` after a job — per-vendor calls, cost summing to 13 (the
waterfall spend), fields filled, queue depth, HTTP RED. New doc `docs/29`. Tracing + dashboards
+ per-tenant metrics (cardinality) honestly deferred.

### 2026-07-01 — Implementation Slice 06 (Go) — webhooks-out (tenant-bound) + OpenAPI
Human approved the webhooks + OpenAPI increment. Added a Dispatcher `OnComplete` hook (fires
after the durable-terminal state, decoupling `job` from `webhook`) and `internal/webhook`: HMAC-
SHA256 signed completion callbacks delivered **tenant-bound** (URL only from the delivering
tenant's registered config, resolved by tenant_id — no cross-tenant PII egress, G1) and
**SSRF-safe** (through a per-tenant egress allow-list, wiring the Slice-05 seam), with bounded
retries (5xx/429 retried, other 4xx terminal) and skip-when-unconfigured. Added `docs/api/
openapi.json` (OpenAPI 3.0.3) + a dependency-free **contract test** binding spec↔impl (every
status code the API returns for a representative request must be declared). Wired the webhook
sender into `cmd/enrichapi` via the hook (env-configured, inert by default). 8 new tests (65
total): sign/verify, signed POST, **tenant-binding (0 cross-tenant hits)**, unconfigured no-op,
bounded 5xx retries, 4xx terminal, OpenAPI contract match. `go build/vet/test/gofmt` clean. New
doc `docs/28`. (No live loopback smoke: the egress guard correctly blocks 127.0.0.1 — by design.)

### 2026-07-01 — Implementation Slice 05 (Go) — egress-proxy / SSRF choke
Human approved the SSRF-choke increment (the #2 security risk). Added `internal/provider/ssrf.go`:
a hardened egress client layering **HTTPS-only + FQDN allow-list** (`hostGuard`) → **key
injection** (Slice 04) → **dial-time IP guard** (`NewEgressTransport` dialer `Control` validates
the resolved IP, refusing metadata/RFC1918/loopback/ULA/link-local/CGNAT/0.0.0.0-8/IPv4-mapped —
DNS-rebinding- and encoding-safe), with redirects re-checked + capped. `ErrSSRFBlocked` classified
non-retryable BAD_REQUEST in adapters. 4 new tests (57 total): the SSRF **corpus** (17 internal
addresses blocked, publics pass, nil fails closed), real loopback dial blocked at the guard,
hostGuard https/allow-list enforcement, full-client metadata refusal. `go build/vet/test/gofmt`
clean. New doc `docs/27`. **Both top-2 risks now enforced in code + tested (G1 + P2 SSRF).**
Documented that a network-level default-deny egress is still required (belt-and-suspenders).

### 2026-07-01 — Implementation Slice 04 (Go) — real provider adapters + egress key-injection seam
Human approved the real-adapters increment. Added `internal/provider/egress.go` (KeyResolver +
AuthInjector RoundTripper injecting the credential by header/query/bearer/basic AS the request
leaves — adapters stay **secret-free**) and `internal/provider/adapters/` with three concrete
API-first vendors: **Hunter** (query api_key; 403→RATE_LIMIT), **Prospeo** (X-KEY header;
402→QUOTA), **Twilio Lookup** (HTTP Basic; 404→NOT_FOUND). Extended the canonical Field vocab
with `first_name`/`last_name`/`full_name` (email-finder match keys; `docs/00` §7 — back-prop).
6 new tests (53 total): per-vendor contract + injection-seam + error-taxonomy, plus
`TestAdapters_EngineIntegration` (two real adapters through Router+Engine fill work_email +
phone_status with G5 provenance). Vendor wire formats honestly marked `UNVERIFIED`/representative
(confirm vs live API before prod; risk localized to Build/Decode). `go build/vet/test/gofmt` clean.
New doc `docs/26`. The egress-proxy slice (SSRF choke) is the natural follow-on — it wraps this seam.

### 2026-07-01 — Implementation Slice 03 (Go) — durable queue + transactional outbox
Human approved the crash-safety increment. Added `internal/durable`: a `fsync`'d framed
write-ahead **Log** (CRC + atomic commit-marked batches + **torn-tail recovery**), a durable
**Store** implementing the **transactional outbox** (job snapshot + publish-intent appended
atomically; intent cleared only on durable-terminal, making execution crash-safe), and a
**Relay** (outbox→queue, at-least-once re-drive on recovery). Refactored the API onto a
`job.Submitter` seam (in-process `QueueSubmitter` OR durable store); `cmd/enrichapi` selects
durable via `DURABLE_LOG`. **At-least-once redelivery is charge-safe via engine G2** (proven
by `TestPipeline_CrashRecoveryExactlyOnceCharge`). 5 new tests (47 total); `go build/vet/test/
gofmt` clean. **Live-verified:** async job survived a full process kill+restart — `GET` after
restart returned the recovered succeeded outcome from the on-disk WAL. New doc `docs/25`;
deferred scope (distributed Kafka/Redpanda log + DB outbox/CDC, field-data durability, log
compaction, group-commit) logged, not hidden.

### 2026-07-01 — Implementation Slice 02 (Go) — API gateway + async job queue
Human approved the API + queue increment. Added `internal/api` (REST gateway: auth→principal
G1, Idempotency-Key writes, per-tenant rate limit, taxonomy→HTTP, validation) + `internal/job`
(tenant-scoped JobStore, bounded two-lane priority Queue with back-pressure shedding, worker-pool
Dispatcher running the engine under the submitter's principal, panic-contained) + `cmd/enrichapi`
(gateway + 8 workers, graceful shutdown). **All five gates preserved across the new surface**;
API-level idempotency added on top of provider-call G2. 20 new tests (42 total); `go build/vet/
test/gofmt` clean; **live HTTP smoke passed** (sync enrich 0.911 email + 13/15 credits w/
provenance; 400 no-key; 401 no-auth; 409 key-reuse; **404 cross-tenant job read**; 429 rate limit).
New doc `docs/24`; deferred scope (durable queue+outbox, real JWT, egress-proxy, webhooks, OpenAPI)
logged, not hidden.

### 2026-07-01 — Implementation Slice 01 (Go) — correctness-gate vertical slice
Human approved implementation (thin vertical slice, Go). Installed Go 1.26.4 locally.
Built an end-to-end enrichment path in `internal/` (`domain`, `tenant`, `provider`,
`router`, `engine`, `store`) + `cmd/enrichd` demo + `migrations/0001_init.sql` (FORCE RLS).
**All five gates enforced in code and each proven by a test** (G1 cross-tenant negative
test = release-blocker; G2 replay = no double call/charge; G3 timeout/retry-bound/breaker;
G4 reserve-before-call never exceeds ceiling + charge-on-success refund; G5 store rejects
bare values). `go build/vet/test/gofmt` clean; coverage 68–89% on covered pkgs. Demo shows a
live waterfall (cheap→premium email fused to 0.911, phone 0.88, 13/15 credits, idempotent
replay = 0 new calls). Documented in `docs/23`; deferred scope (Postgres integration test,
egress-proxy, queue, API, real adapters, calibration) logged, not hidden. New doc `docs/23`.

### 2026-07-01 — Planning Completion Gate — adversarial review + fixes
5-reviewer adversarial audit (`wf_15689f67-653`) of the whole repo. **5 blocking FAILs found and fixed:**
(B1) adapter-holds-secret contradiction → auth-descriptor + egress key injection; (B2) idempotency-key
canonicalized across skill/`04`/`09`/`10`/`erd`; (B3) ClickHouse tenant isolation compensating control
(row policy + mandatory predicate + CI test); (B4) outbound webhook allow-list made tenant-bound (closes
cross-tenant PII egress); (B5) ADR index + footer corrected (0015). WARNs addressed: intent-lane G3+egress,
outbox boundary enumeration + CDC relay, SSRF IP-encoding-bypass, audit immutability (hash-chain+WORM),
Little's-Law harmonized (350 ms), glossary "account" note, SSOT diagram map, tracker de-dup. Accepted gaps
(GraphQL/gRPC deferrals, artifact-level items, QS-TMP-1, secrets-backend, UNVERIFIED numbers) logged in
`IMPLEMENTATION_PROGRESS.md` §PCG. **Gate = PASS; awaiting human approval to implement.**

### 2026-07-01 — Phases 17–22 (ops & product) — auto-advance batch
- `17-Dashboard-Planning.md` — every panel mapped to a backing service/table; RBAC/ABAC scope.
- `18-Security.md` — consolidated model: two-layer tenant isolation (P1), SSRF (P2, ref `13`), RBAC/ABAC,
  Vault/KMS, residency + compliance map (incl. data-broker/DNC/consent), STRIDE, DR (RPO≤5m/RTO≤1h).
- `19-Deployment.md` + `deployment.mmd` + `infrastructure.mmd` + **ADR-0015** (portability-first, AWS
  reference, regional cells, blue-green/canary, default-deny egress zones).
- `20-Monitoring.md` — golden signals + enrichment KPIs (hit-rate/lift/cost-per-match) + SLOs + security telemetry.
- `21-Testing.md` — negative gate tests (G1–G5, release blockers) + load test (turns throughput
  UNVERIFIED→VERIFIED) + SSRF corpus + chaos + DR drills; every `UNVERIFIED` assumption mapped to a test.
- `22-Future-Roadmap.md` — deferred backlog (incl. QS-TMP-1 Temporal spike).
- **All 22 planning docs now IN-REVIEW; 9 diagrams complete; ADRs 0000–0015.** → Planning Completion Gate.

### 2026-07-01 — Phase 10 (Queue System) — auto-advance
- `10-Queue-System.md` + `queue-flow.mmd` + `retry-flow.mmd` from a 7-technology cited tradeoff
  workflow (`wf_2013b0cd-df8`). **Two orthogonal decisions:** **ADR-0013** async transport = Kafka-
  protocol log (Redpanda preferred) — chosen for lag back-pressure + replay + multi-cloud portability
  (SQS/Pub/Sub rejected as single-cloud; RabbitMQ wrong back-pressure model); **ADR-0014** orchestration
  = Temporal durable execution (deletes hand-rolled Saga/outbox/checkpoint + native tenant fairness),
  **cost-gated** on an Action-volume spike (**QS-TMP-1**, flagged to human) with documented fallback =
  hand-rolled Saga+outbox on the same backbone. Redis KV = idempotency store.
- Back-propagated: `05` workers-as-Temporal-workers; `09` §5 checkpoint via Temporal (both conditional).

### 2026-07-01 — Phases 5–9, 11–16 (core architecture) — auto-advance batch
Per human-approved cadence (auto-advance 5–16, stop only for FAILs/decisions), authored from the
established ADRs; each doc carries its own recorded gate checklist. Phase 10 (Queue) pending its
tradeoff-research workflow.
**Added / rewritten**
- `05-Microservices.md` (+ `dependencies.mmd`) — module/service catalog + boundary rules.
- `06-Database-Architecture.md` (+ `erd.mmd`) + **ADR-0011** (Postgres RLS-pool + Redis + ClickHouse).
- `07-API-Gateway.md` + **ADR-0012** (REST + webhooks external, gRPC internal, GraphQL deferred).
- `08-Waterfall-Orchestrator.md` — full routing/plan spec (answers every ordering question).
- `09-Execution-Engine.md` — deterministic gate spine (G2/G3/G4 re-checked per call; G5 structural).
- `11-Scaling-Strategy.md` — Little's-Law sizing, per-provider budgets, finite autoscaling.
- `12-Provider-Key-Management.md` — key pools, health, continuity, correlation graph.
- `13-Proxy-Management.md` — SSRF-safe egress choke (top-2 risk), key injection at proxy.
- `14-Intent-Engine.md`, `15-Verification-Engine.md` — providers cited from `03`.
- `16-Cost-Optimization.md` — ceilings, charge-on-success, cache-before-reveal.

### 2026-07-01 — Phase 4 (System Architecture) complete → at GATE
**Added**
- `docs/04-System-Architecture.md` — end-to-end system design via a 3-proposal/3-judge design panel
  (`wf_2099540b-a5f`). Winner: **hybrid modulith control-plane + elastic stateless data-plane** (best
  cost/p95 balance meeting scale + isolation), with microservices-proposal hardening grafted in.
- **ADR-0010** — architecture style + topology + sync/async boundary + two-layer tenant identity +
  keys-injected-at-egress + config-as-versioned-data + regional cells.
- Diagrams: **replaced** `architecture.mmd` (real component graph), **added** `api-flow.mmd` +
  `event-flow.mmd`.

**Structural gate enforcement documented:** G1 (FORCE RLS + signed principal context), G2 (Postgres
ledger + Redis fast-path + seeded RNG), G3 (Redis-shared breakers), G4 (atomic pre-flight reservation),
G5 (merge-then-write with NOT NULL provenance FK), SSRF (default-deny egress; only proxy has internet).

**Back-propagated:** `05` MS-2 decided (modulith); `06` DB-1 provisional (Postgres RLS-pool + ClickHouse)
to ratify in Phase 6; `10` QS-1 placement decided, engine to ratify in Phase 10.

**Open at gate:** engine choices (datastore SA-3, queue SA-4) explicitly deferred to their phase ADRs.

### 2026-07-01 — Phase 3 (Provider Research & Matrix) complete → at GATE
**Added**
- `docs/03-Provider-Research.md` — 28 providers researched + adversarially citation-verified via
  workflow `wf_f5d38fad-6f3` (56 subagents, ~1.84M tokens, 798 fetches; 672 claims, 38 downgraded).
  Combined with 18 Phase-1 providers → **46-provider roster** across all 22 required categories.
  Includes the **capability→provider coverage map + per-field seed waterfall ordering** (feeds ADR-0007).
- **ADR-0009** — provider inclusion/exclusion criteria: resolves the "scraped-provenance ⇒ exclude"
  inconsistency (Apollo/ZoomInfo also ingest public-web data yet are ACTIVE). 2 hard EXCLUDED
  (Proxycurl — LinkedIn litigation/wind-down; Datanyze — defunct/absorbed); 3 DEPRIORITIZED
  (Kaspr, ContactOut, Coresignal) pending a human policy decision (**PR-EXCL-1**).

**Back-propagated (audit loop)**
- `08` OR-4 cold-start ordering now seeded from `03` §3; `12` provider correlation/ownership graph
  (copy-discount for ADR-0005); `14` intent/signal providers confirmed; `15` verification providers
  confirmed; `18` provenance/compliance gating for DEPRIORITIZED providers.

**Open at gate:** **PR-EXCL-1 needs a human policy decision**; all latency `UNVERIFIED` (load test);
identity/domain-intel provider specifics provisional (heavy downgrades).

### 2026-07-01 — Phase 2 (Waterfall Methodology) complete → at GATE
**Added**
- `docs/02-Waterfall-Research.md` — 5 methodology tracks (identity resolution, confidence aggregation,
  truth discovery/merge, cost-aware ordering, learned routing) researched + adversarially
  citation-verified via workflow `wf_8ebd6dba-440` (10 subagents, ~421K tokens, 199 fetches; 46
  methods, 2 downgraded, **0 hallucinated references**). Includes the adopted end-to-end pipeline.
- `diagrams/enrichment-pipeline.mmd` — canonical per-record methodology pipeline.
- **ADR-0004** (tiered identity resolution), **ADR-0005** (calibrate-then-fuse confidence + SPRT),
  **ADR-0006** (deterministic online merge + PROV), **ADR-0007** (Pandora reservation-value ordering),
  **ADR-0008** (Thompson routing inside deterministic G3/G4 gate).

**Governing invariant adopted:** "model proposes, deterministic gate disposes" — learned components
rank/propose; the Execution Engine re-enforces G3/G4 before every call; merge is rule-deterministic.

**Back-propagated (audit loop)**
- `08` ordering=Pandora + routing=Thompson + SPRT stop (OR-2/OR-3 now decided).
- `09` calibrate→fuse→SPRT + deterministic merge + tiered identity references.
- `06` new model additions (identity_clusters, calibrators, reliability weights, reservation values,
  bandit posteriors, W3C PROV field lineage).

**Open at gate:** WQ-1…WQ-11 (`ACCEPTED`) parameterize the chosen methods; resolved with measured
provider data (`03`) or the implementation feedback loop.

### 2026-06-30 — Phase 1 (Market Research) complete → at GATE
**Added**
- `docs/01-Market-Research.md` — 18 competitors researched + adversarially citation-verified via
  workflow `wf_6a361ade-28c` (36 subagents, ~1.08M tokens, 464 web fetches). Includes a comparison
  matrix, per-competitor cited entries with verification markers, executive synthesis, and an
  architecture-takeaways mapping. 27 of 144 sampled citations were downgraded to `UNVERIFIED`.

**Findings → decisions**
- Only Clay + BetterContact are true waterfall orchestrators; all other surveyed vendors are leaf
  sources with region/segment gaps → validates building an orchestrator with regional ordering.
- Clearbit standalone Enrichment API `DEPRIORITIZED` (sunset 2026, HubSpot-only).

**Back-propagated (audit loop)**
- `api-integration` skill: added 402=credit-exhaustion→failover + Hunter 403=throttle quirk + ingest
  quota headers.
- `08` per-(provider,field,region) confidence ordering + search/preview→reveal.
- `09` defensive field typing + provider-aware chunking + HMAC webhook fan-in.
- `12` provider supply-continuity health signal; `16` charge-on-success + Data-Credits/compute split
  + cache-before-reveal; `18` compliance map += data-broker registration/DNC/consent; `20` waterfall
  KPIs (hit-rate, incremental lift, cost-per-match) + continuity monitoring.

**Open at gate**
- 27 downgraded claims now `UNVERIFIED` (honest gaps, `ACCEPTED-RISK`); `✓` (un-re-checked) claims
  to be deepened in Phase 3 for chosen providers.

### 2026-06-30 — Phase 0 (Bootstrap) complete
**Added**
- Repository scaffolding: `/docs`, `/adr`, `/skills`, `/agents`, `/commands`, `/diagrams`; git init; `.gitignore`.
- `docs/README.md` — documentation root, status + verification legends, gate sequence.
- `docs/00-Project-Overview.md` — scope, **canonical Glossary (§7)**, throughput target as a tested
  assumption with supporting math, success criteria, highest-risk areas (tenant isolation + SSRF).
- `docs/00b-Tooling-And-Agents.md` — index + contract for all Phase 0 tooling.
- Skills: `enrichment-discipline`, `provider-research`, `waterfall-correctness`, `api-integration`,
  `doc-consistency`.
- Agents: Research, Architecture Reviewer, Gap-Analysis, Security Auditor, Implementation,
  Cost/Scale Reviewer.
- Commands: `/provider-audit`, `/architecture-review`, `/security-audit`, `/scale-check`,
  `/gap-analysis`, `/gate-check`.
- ADRs: 0000 (template), 0001 (record decisions), 0002 (API-first, no scraping), 0003 (plan-first
  gated process). ADR index in `adr/README.md`.
- Trackers: `docs/IMPLEMENTATION_PROGRESS.md`, this changelog.
- Doc stubs `01`–`22` with consistent headers, status, and Open-items tables.
- `diagrams/architecture.mmd` placeholder (to be replaced in Phase 4).

**Decisions**
- API-first only; no scraping/automation/manual workflows (ADR-0002).
- Plan-first, gate-driven process with human approval at gates (ADR-0003).

**Notes / deferred**
- All per-provider/competitor facts remain `UNVERIFIED` until cited in Phases 1/3.
- Throughput target (2,000 rec/s) is an engineering **assumption** pending load test (Phase 21).
- Optional `.claude/` mirror of skills/commands deferred as an enhancement.
