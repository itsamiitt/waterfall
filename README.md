# Waterfall Enrichment Engine

An API-first, multi-tenant B2B data-enrichment engine. Given a partial subject (a person or
company) and a set of wanted fields, it queries a **waterfall** of provider APIs — cheapest and
most-likely-to-succeed first — and stops as soon as each field is filled to the requested
confidence, under a hard per-request cost ceiling. Every returned value carries full
provenance: which provider produced it, at what confidence and cost, under which idempotency key.

Built in **Go 1.26 with the standard library only** — no third-party modules (see `go.mod`: no
`require` block). That includes a from-scratch PostgreSQL wire-protocol client (startup, simple +
extended query, SCRAM-SHA-256, TLS), a dependency-free Prometheus exposition, JWT verification,
isotonic calibration, and a Thompson-sampling router. The point is auditability: every byte on
the wire and every security decision is in this repo, not a dependency.

> **Status (2026-07-01):** planning phases 0–22 complete (`docs/00`–`docs/22`, 16 ADRs); **18
> implementation slices** landed and verified — see [`docs/IMPLEMENTATION_PROGRESS.md`](docs/IMPLEMENTATION_PROGRESS.md).
> The engine, gateway, durable queue, and PostgreSQL persistence run and are tested live. What is
> and isn't production-ready is stated honestly per slice; nothing here is claimed that a test
> doesn't back.

## The five correctness gates

The whole system is organized around one invariant — **"the model proposes, a deterministic gate
disposes."** A learned router may *suggest* which provider to try; five deterministic gates decide
what actually happens, and each is enforced in code and tested:

| Gate | Guarantee | Where |
|------|-----------|-------|
| **G1 — Tenant isolation** | `tenant_id` flows only from the authenticated principal, never the request body; Postgres **row-level security** (FORCE RLS) makes cross-tenant reads impossible even with a bug. | `internal/tenant`, `internal/pgstore`, `migrations/0001` |
| **G2 — Idempotency** | An idempotency ledger is written **before** every provider call; a retry or redelivery never double-charges or double-executes. | `internal/store`, `internal/pgstore`, `internal/engine` |
| **G3 — Bounded execution** | Every provider call is wrapped in timeout + circuit breaker + capped retry; no unbounded work. | `internal/provider`, `internal/engine` |
| **G4 — Cost ceiling** | Reserve-then-charge-on-success against a per-request ceiling; the ceiling can never be exceeded, even under concurrency. | `internal/store`, `internal/pgstore` |
| **G5 — Provenance** | Every filled field records provider, confidence, cost, idempotency key, and timestamp. | `internal/domain`, `internal/engine` |

## Architecture

```
        HTTP (JWT auth → principal; Idempotency-Key; per-tenant rate limit)
          │
   internal/api ──────────────► internal/job (bounded queue + worker pool)
          │                              │
          │                    durable delivery, most-durable-first:
          │                      Postgres transactional outbox  (internal/pgoutbox + Relay)
          │                      │  file-WAL  (internal/durable)
          │                      │  in-process
          ▼                              ▼
   internal/engine  ── executes ──►  Router plan (internal/router, bandit-scored)
     (G1–G5 gates)                        │
          │                       internal/provider  ── HTTP ──►  vendor APIs
          │                         adapters + egress key-injection + SSRF choke
          ▼
   internal/store  ⇄  internal/pgstore  (field versions, idempotency ledger, cost ledger; RLS)
```

Secrets never touch an adapter: adapters attach an *auth descriptor*, and the egress
`AuthInjector` resolves and injects the credential as the request leaves the trust boundary. All
egress passes an SSRF choke (HTTPS-only + host allow-list + dial-time IP guard).

## Quickstart

Requires the Go toolchain (1.26+). Everything below runs **offline** — the default providers are
in-memory fakes, so no keys or network are needed.

```sh
# 1. Run the full unit suite (94 tests, no external services).
go test ./...

# 2. One-shot engine demo: enrich one record through two mock providers, print the outcome
#    with full provenance + cost accounting.
go run ./cmd/enrichd

# 3. Run the HTTP gateway in memory mode and make a request.
go run ./cmd/enrichapi &          # listens on :8080; uses in-memory store + mock providers
curl -s -X POST 'http://127.0.0.1:8080/v1/enrichments?mode=sync' \
  -H 'Authorization: Bearer acme-token' -H 'Idempotency-Key: demo-1' \
  -H 'Content-Type: application/json' \
  -d '{"subject":{"id":"p1","known":{"company_domain":"acme.com","first_name":"jane","last_name":"doe"}},
       "want":["work_email"],"confidence_target":0.7,"cost_ceiling":100,"config_version":"v1"}'
```

Or run all of it — build, unit tests, the engine demo, and a live API round-trip — with one
command:

```sh
bash scripts/demo.sh
```

### Running against real PostgreSQL

The gateway switches to Postgres (RLS-enforced store + durable outbox) when `POSTGRES_DSN` is set;
with `POSTGRES_ADMIN_DSN` it also runs migrations and provisions the app/relay roles at startup.
The live test harnesses (need a local PostgreSQL 17) prove this end-to-end:

```sh
bash scripts/run-rls-test.sh        # 11 build-tagged integration tests on an ephemeral cluster:
                                     #   RLS isolation, G2/G4 ledgers, SCRAM, TLS, outbox crash-
                                     #   safety, dead-letter + redrive, migration runner, E2E
bash scripts/crash-recovery-test.sh # hard-kills the real binary mid-flight and proves every
                                     #   in-flight job recovers from the outbox on restart
```

## Testing story

- **Unit suite** (`go test ./...`, 94 tests): the gates, router, calibration, bandit, JWT, SSRF,
  metrics, the stdlib Postgres/SCRAM crypto vectors, and the API contract — all offline.
- **Live integration** (`-tags integration`, gated on `WATERFALL_PG_DSN`): 11 tests against a real
  PostgreSQL 17, covering everything that can only be proven against a live database.
- **Crash-recovery harness**: submits jobs, `kill -9`s the real binary mid-flight, restarts, and
  asserts every job completes with no double-charge (G2 across redelivery).

## Repository map

| Path | What's there |
|------|--------------|
| `cmd/enrichapi` | HTTP gateway + async worker pool (the service) |
| `cmd/enrichd` | Offline one-shot engine demo |
| `internal/engine` | The G1–G5 execution engine |
| `internal/router` | Adaptive plan builder (bandit-scored) |
| `internal/provider` | Adapter framework, egress key-injection, SSRF choke, real vendor adapters |
| `internal/pg`, `pgstore`, `pgoutbox`, `pgmigrate` | Stdlib Postgres client, RLS store, transactional outbox, migration runner |
| `internal/store`, `domain`, `tenant`, `job` | Ledgers/read-model, types, principal, queue |
| `internal/config` | Env config loader + validation (fail-fast at startup) |
| `internal/auth`, `api` | JWT verification, HTTP surface (incl. `/readyz` + startup self-check) |
| `docs/` | Design specs (`00`–`22`), implementation slices (`23`–`40`), trackers, OpenAPI |
| `adr/` | Architecture Decision Records |
| `migrations/` | Ordered SQL migrations (RLS, outbox, DLQ) |
| `scripts/` | Demo + live test harnesses |

Start with [`docs/README.md`](docs/README.md) for the full documentation index, and
[`docs/IMPLEMENTATION_PROGRESS.md`](docs/IMPLEMENTATION_PROGRESS.md) for the slice-by-slice status.

## What's honestly not here yet

Tracked per slice in `docs/IMPLEMENTATION_PROGRESS.md`; the notable gaps: real vendor API calls
(the adapter wire shapes are pinned as **UNVERIFIED** fixtures pending an authorized key), a CI
pipeline, graceful SIGTERM drain, cross-tenant operator tooling, and OpenTelemetry tracing. Each
is called out where it matters rather than papered over.
