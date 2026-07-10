# ADR 0029 — Embeddings / RAG on Postgres — deferred

- **Status:** Accepted (decision: **defer**)
- **Date:** 2026-07-09
- **Deciders:** Lead Enterprise Solutions Architect, Staff ML Engineer, Principal Backend Engineer
- **Phase:** R&I (Research & Intelligence) · **Guards:** ADR-0016, ADR-0022

## Context
An AI research platform is often expected to carry an embeddings / vector-retrieval (RAG) layer:
semantic dedup of collected documents, similarity search over a Dossier corpus, retrieval-augmented
prompting. The pull is real, but three constraints make **now** the wrong time to build it:

1. **Stdlib-only Go (ADR-0016/0022).** A managed vector-DB client or an embeddings SDK would be the
   first third-party Go dependency; even `pgvector` changes the Postgres deployment surface (an
   extension the RLS-pool cluster must ship and the migration runner must enable).
2. **Free-first budget (ADR-0026).** Embedding every collected document adds a per-token cost with no
   proven product need yet — the core Dossier is assembled from structured provider APIs + targeted
   LLM synthesis, which needs **no** vector store.
3. **The core spine doesn't require it.** Deterministic keys (`company_domain`, normalized name, CIK,
   LEI) + the G2 idempotency ledger + Postgres full-text where needed cover dedup and retrieval for
   the v1 research product.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. Adopt a vector DB / embeddings SDK now | full semantic retrieval | new Go dep + new infra + per-token embedding cost, with no proven need | capability vs dep/cost discipline |
| B. Enable `pgvector` now | Postgres-native, no Go dep | changes the DB deployment surface; still needs an embedding provider + cost; premature | native vs premature complexity |
| **C. Defer; record a revisit trigger (chosen)** | keeps zero-dep + free-first intact; nothing premature | semantic dedup/similarity unavailable until built | discipline vs a deferred capability |

## Decision
**Defer embeddings/RAG.** No vector-DB client, no embeddings SDK, and **no `pgvector`** is introduced
now. v1 dedup/retrieval uses deterministic keys + the idempotency ledger + Postgres full-text.

**When the trigger fires, the preferred path is Postgres-native, behind an interface** (mirroring the
ADR-0011 Redis/ClickHouse "design-target behind a Go interface" pattern): either `pgvector` enabled
via the migration runner, or brute-force cosine over `float4[]` columns in stdlib Go for small corpora
— **decided in a future ADR**, not defaulted to a managed vector DB.

**Revisit trigger (any of):** (a) the Dossier/source corpus exceeds a scale where deterministic +
full-text dedup measurably misses duplicates; (b) a product feature requires semantic similarity
(e.g. "companies like this"); (c) LLM-context grounding needs retrieval over stored sources beyond
what targeted provider calls supply. The trigger and its evidence are tracked in
`docs/research-intelligence/15` (roadmap).

## Rationale
Deferring protects the two properties that make this platform auditable and cheap — zero Go deps and
free-first — against a capability the core product does not yet need. Recording the trigger and the
Postgres-native preference means the deferral is a **decision**, not an omission: when the need is
real and measured, the path is already scoped and stays inside the dependency discipline.

## Consequences
- **Positive:** no new dependency or infra; no premature embedding cost; the zero-dep audit stays clean.
- **Negative / accepted:** no semantic dedup/similarity in v1; a future ADR + build is required when the
  trigger fires. Accepted.
- **Follow-ups / new ADRs triggered:** a future "embeddings/RAG on Postgres" ADR when a trigger is met.

## Verification
- **Zero-dep audit** (`/architecture-review`): `go.mod` gains no vector/embeddings dependency; no
  `pgvector` in the migrations; grep finds no embedding client.
- **Product check:** the v1 Dossier assembles and dedups correctly with deterministic keys + full-text
  (no vector store in the assembly path).
