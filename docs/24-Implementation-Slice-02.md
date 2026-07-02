# 24 — Implementation Slice 02: API gateway + async job queue (Go)

**Status:** `IMPLEMENTED` (tests green + live HTTP smoke) · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`23`](23-Implementation-Slice-01.md) · **Approved by:** human (2026-07-01)

> The second increment: a real HTTP surface and an asynchronous execution path in front
> of the Slice-01 engine — without weakening any correctness gate.

## 1. What this slice adds
- **REST gateway** (`internal/api`, stdlib `net/http`): `POST /v1/enrichments` (sync via
  `?mode=sync` or async), `GET /v1/enrichments/{id}`, `GET /v1/records/{subjectID}`, `GET /healthz`.
- **AuthN → principal (G1):** pluggable `Authenticator`; the verified principal is the
  **only** source of `tenant_id` — request bodies cannot set it.
- **Idempotency-Key on writes** (docs/07, ADR-0012): deterministic job id `= hash(tenant, key)`;
  a repeat returns the same job; the same key with a **different body → 409**.
- **Per-tenant rate limiting** (token bucket, docs/18 §6) → 429.
- **Async job queue** (`internal/job`): bounded two-lane (premium/bulk) queue + worker-pool
  **Dispatcher**; **back-pressure** sheds (429) when saturated (docs/11 §4); jobs run under
  the submitter's principal (G1); panics in a runner are contained (job → failed).
- **Runnable:** `go run ./cmd/enrichapi` (gateway + 8 workers).

## 2. Packages added
| Package | Responsibility |
|---------|----------------|
| `internal/job` | `Job`, tenant-scoped `Store`, bounded priority `Queue`, worker-pool `Dispatcher`, deterministic id/fingerprint |
| `internal/api` | `Authenticator` (+ static impl), `RateLimiter`, `Server` (routing + middleware), handlers, DTOs/validation |
| `cmd/enrichapi` | Wires gateway → queue → engine; graceful shutdown |

## 3. Gates preserved across the new surface
| Gate | How it survives the API/queue layer |
|------|--------------------------------------|
| **G1** | `tenant_id` from the authenticated principal only; captured on the Job and carried to the worker ctx; every job/record read tenant-scoped → cross-tenant job read is **404** |
| **G2** | provider-call idempotency unchanged (engine); **plus** API-level write idempotency (Idempotency-Key → same job, no re-run) |
| **G3/G4/G5** | unchanged — the worker calls the same Slice-01 engine, which enforces bounded calls, the cost ceiling, and provenance |

## 4. Tests (20 new; 42 total in the module)
`internal/job`: queue shed/lane-preference/done, dispatcher run/async/error/**panic-contained**,
JobStore **tenant isolation**, deterministic id. `internal/api`: sync outcome+provenance,
async submit→poll→succeeded, **401** no-auth, **400** no-Idempotency-Key, **422** unknown
field, **idempotent replay = no second provider call**, **409** key reuse, **404** cross-tenant
job read, **429** rate limit, record read, healthz.

## 5. Live HTTP smoke (verified)
`go build ./cmd/enrichapi` → real requests: healthz ok; sync enrich returned `work_email`
fused to **0.911** (target met via cheap→premium) + phone 0.88, **13/15 credits**, full
provenance; missing key → 400; no auth → 401; async submit → `queued` job id; **cross-tenant
GET → 404**.

## 6. Honestly out of this slice
- Real JWT/mTLS verification (the `Authenticator` seam is here; static tokens stand in).
- Durable queue (Kafka/Redpanda, ADR-0013) — in-process queue is the seam; jobs not yet
  crash-durable. Outbox/CDC (docs/10 §4) lands with the durable queue.
- Webhooks-out, batch/bulk endpoints, OpenAPI spec + contract tests (docs/07 §5).
- Egress-proxy (SSRF choke), Postgres store + RLS integration test (Slice-01 §5 carry-over).

## 7. Reviewer result
| Check | Result |
|-------|--------|
| All five gates preserved across API + async | PASS |
| tenant_id from principal only; cross-tenant read 404 | PASS |
| Idempotency-Key enforced; reuse→409; replay no re-run | PASS |
| Back-pressure sheds (429), doesn't grow unbounded | PASS |
| `go build/vet/test/gofmt` clean; live HTTP smoke passed | PASS |
| Deferred scope logged, not hidden (§6) | PASS |

**Gate:** slice `IMPLEMENTED`; proceeds to the next increment on request.
