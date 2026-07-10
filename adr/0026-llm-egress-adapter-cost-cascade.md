# ADR 0026 — LLM-as-egress-adapter + deterministic AI cost cascade

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** Lead Enterprise Solutions Architect, Principal Backend Engineer, Staff ML Engineer, Staff Security Engineer
- **Phase:** R&I (Research & Intelligence) · **Extends:** ADR-0007, ADR-0008, ADR-0024 · **Builds on:** ADR-0016, ADR-0022

## Context
The AI Research Engine (`docs/research-intelligence/04`) needs LLM inference to summarize, extract,
classify, and normalize collected data into a Dossier. **No LLM code exists in the repo today** —
this is greenfield. Two hard constraints frame the choice:

1. **The Go backend is stdlib-only** (ADR-0016 / ADR-0022): no third-party Go module without a
   superseding ADR. The 145 existing provider adapters already prove that non-trivial vendor APIs
   (including a from-scratch Postgres wire client, JWT, SCRAM) are reachable with stdlib `net/http`
   + `encoding/json`.
2. **Budget is limited.** The product requirement is to run **primarily on free / low-cost models**
   (OpenRouter's free-model pool), escalating to paid models only when necessary, under the G4 cost
   ceiling.

An LLM chat/completions call is **plain HTTPS + JSON** — the OpenAI-compatible schema that OpenRouter,
OpenAI, and most gateways expose. It is indistinguishable, at the transport layer, from the provider
calls the egress tier already makes. The governing platform invariant also applies: **"the model
proposes, a deterministic gate disposes"** — an LLM (like the bandit router) may *suggest*, but a
deterministic gate must decide every spend and action.

Pricing/limits/free-model availability are `UNVERIFIED` here; cited in `docs/research-intelligence/11`.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. Adopt an LLM SDK (e.g. a vendor client lib) | fast to start; batteries included | **breaks the zero-dependency rule** (first third-party Go dep); pulls transitive deps; auth/keys leak out of the egress tier | convenience vs the repo's core auditability value |
| B. Self-host open models (GPU fleet) | no per-token vendor cost; data residency | large infra + ops cost; contradicts "free-first, low-cost"; new failure domain | control vs cost/ops |
| **C. LLM providers as egress adapters (chosen)** | zero new Go dep; reuses egress/key-injection/breaker/cost/idempotency verbatim; keys stay at the egress tier; free-first cascade fits ADR-0007 | must hand-write the (small) adapters + a struct-based response validator; nondeterministic outputs need a caching contract | reuse/discipline vs a little more adapter code |

## Decision
**Model every LLM as an ordinary Provider adapter.** An LLM call reuses `internal/provider.HTTPAdapter`
(or `AsyncHTTPAdapter`), emits `AuthDescriptor{Scheme: AuthBearer, KeyPoolSelector: ...}`, and the
egress `AuthInjector` attaches the credential as the request leaves the trust boundary — **secrets
never touch the adapter**, exactly as today. New registry **category `llm`** (one slug). Example
adapters: `openrouter` (free-model pool, primary), `openrouter-paid`, `openai`, `anthropic`.

- **Gates.** G3 via a `CallPolicy` override (`{Timeout: 60–90s, MaxAttempts: 1}`, the ADR-0024
  async-adapter shape) + breaker; G4 reserve-**estimated**-tokens before the call, charge-**actual**
  tokens on success (existing reserve/charge, with an over-reserve buffer + reconcile); G2 idempotency
  key = `hash(tenant, subject, task_type, model_slug, prompt_version, input_hash, config_version)`;
  G1 tenant isolation on all AI accounting rows; G5 provenance on every AI-derived value
  (`source_type = ai_inference`, model, tokens, cost, prompt_version, confidence) with **losing
  candidate answers retained**.
- **Deterministic cost cascade (the heart of this ADR).** Candidate models are ordered
  **free → mid → paid** by an ADR-0007 Pandora reservation-value cascade; the accept / escalate / stop
  decision (FrugalGPT-style) is **disposed by a deterministic gate over deterministic signals only**:
  (a) **JSON-schema-valid** output, (b) **G4 budget** remaining, (c) **attempt count** ≤ policy,
  (d) **cross-field / cross-model agreement**. The decision is **never** made from an LLM's
  **self-reported confidence**, and the model **never chooses which tool/provider to call** — the
  orchestrator DAG (ADR-0027 / `docs/research-intelligence/04`) decides that. `internal/bandit`
  (Thompson, guardrailed, ADR-0008) may *propose* the model ranking; the gate disposes.
- **Output validation is struct-based, stdlib-only.** Responses are validated by unmarshalling into a
  typed Go struct + explicit field checks (the `internal/api/dto.go` validation pattern) — **not** a
  general JSON-Schema engine and **not** a third-party validator. A `json_validation` task re-asks the
  model on failure, capped by `MaxAttempts`.
- **Routing + prompts are versioned config, not new tables.** LLM routing/fallback policy = a
  `config_versions` kind **`llm_route`**; prompt templates = a `config_versions` kind **`ai_prompt`**
  (platform-owned under the sentinel `platform` Tenant, with optional per-Tenant override) — both via
  `internal/dash/configver`, joining `routing_policy`/`waterfall_workflow`. The prompt **version** is
  part of the G2 key, so editing a prompt mints a new key (no stale cache). The models themselves are
  **provider-catalog rows** projected from `registry.go` (ADR-0023) — there is **no** separate
  `llm_models` table.
- **Nondeterminism contract.** LLM output is not bit-reproducible. G2 here means **cache-on-first-
  success** keyed as above (temperature pinned low / seed where the model honors it), **not**
  reproduce-identically. Re-issuing the same key returns the stored result.

## Rationale
Option C keeps the platform's central value — every byte on the wire and every secret decision is in
this repo, not a dependency — while giving the AI layer the full egress/gate/cost/idempotency
machinery for free. Treating LLMs as providers also means the **free-first cost cascade is not new
engineering**: it is the ADR-0007 reservation-value ordering the router already embodies, applied to
models instead of data vendors. We explicitly rejected the SDK (Option A) because the first
third-party Go dep would erode auditability and, worse, tends to move auth handling out of the single
egress custodian. We rejected self-hosting (Option B) because it contradicts the free-first budget
posture. The one genuine cost — nondeterministic outputs — is contained by a precise cache-on-first-
success contract rather than pretending LLM calls are reproducible.

## Consequences
- **Positive:** zero new Go dependency; keys stay at the egress tier; per-model spend, breaker state,
  and idempotency are first-class; free models carry the default load, paid models are a gated escalation.
- **Negative / accepted:** we hand-write the LLM adapters and a struct validator; G4 must reserve on a
  token *estimate* and reconcile on actual (buffer + nightly reconcile); LLM caching is
  cache-on-first-success, not reproducibility. Accepted.
- **Follow-ups / new ADRs triggered:** ADR-0027 (computed intent consumes LLM-proposed signals);
  ADR-0029 (embeddings/RAG deferred — keeps vector clients out); the token-estimator + buffer % is
  specified in `docs/research-intelligence/11`.

## Verification
- **Zero-dep audit:** `go.mod` has no new `require`; grep finds no LLM/vendor SDK import; the JSON
  validator is stdlib struct-based (`/architecture-review` + `/security-audit`).
- **Gate mapping:** an integration test drives an LLM adapter through `provider.Call` — G3 bounded
  (CallPolicy honored), G4 reserve/charge on tokens, G2 idempotent replay returns the cached result,
  G5 provenance row written with `source_type=ai_inference`.
- **Deterministic-cascade test:** with a scripted fake LLM, escalation fires **only** on schema-invalid
  output / budget / attempt-count — never on a model's self-reported confidence; a model-emitted
  "call tool X" instruction is ignored (no model-driven tool execution).
- **Cost:** free-model-first is observable in `llm_cost_usd` telemetry; paid-model share stays below
  the configured cap. Per-token pricing stays `UNVERIFIED` until measured (`docs/research-intelligence/11`).
