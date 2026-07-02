# 33 — Implementation Slice 11: full-stack end-to-end test (live) (Go)

**Status:** `IMPLEMENTED` (**full-stack E2E passed live on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`32`](32-Implementation-Slice-10.md) · **Canonical spec:** [`04`](04-Data-Flow.md), [`21`](21-Testing.md) · **Approved by:** human (2026-07-01)

> One black-box test that drives the **whole wired system** — a real signed JWT → HTTP
> gateway → async queue + worker pool → Execution Engine → **live PostgreSQL (RLS)** → signed
> webhook — and asserts the correctness gates hold *across* the system, not just in unit
> isolation. This is the "does it actually work end to end" proof the earlier slices earn.

## 1. What is wired (nothing mocked except the vendor HTTP)
| Layer | Real component |
|-------|----------------|
| Auth | `auth.Verifier` (HS256) via `api.JWTAuthenticator` + `WriteScope` |
| Gateway | `api.Server.Handler()` — idempotency-key, validation, rate limit, principal binding |
| Async | `job.Queue` + `job.Dispatcher` worker pool + `QueueSubmitter` |
| Engine | `engine.Engine` (G1–G5 spine, router plan) |
| **G5 store** | **`pgstore` on live PostgreSQL with FORCE RLS** |
| G2/G4 ledgers | in-memory (Postgres port is a later slice) |
| Webhook | `webhook.Sender` (HMAC-signed, tenant-bound) → loopback sink |

Only the provider *vendors* are deterministic fakes (they count calls, enabling the G2
assertion). Everything between the JWT and the database is the production code path.

## 2. Gates asserted across the system
| Gate | Assertion (black-box, over HTTP) | Result |
|------|----------------------------------|--------|
| **G1** tenant isolation | after a tenant-e2e job fills `work_email`, `GET /v1/records/subj-1` as **tenant-other** returns **0 fields** — enforced by live RLS, not app code | PASS |
| **G2** idempotency | a **second** job (different idempotency key) for the same record+field+params triggers **0 new provider calls** (served from the ledger) | PASS |
| **G4** cost ceiling | a job with `cost_ceiling: 2` against a 6-credit premium provider commits **≤ 2** (no overspend) | PASS |
| **G5** provenance | the value read back from Postgres carries provider + idempotency_key + confidence | PASS |
| Delivery | a **signature-valid**, tenant-bound webhook is delivered on completion (verified with the tenant secret) | PASS |

Runs in ~0.18s once Postgres is up. Added to `scripts/run-rls-test.sh` alongside the RLS
release-blocker test.

## 3. Honest notes
- **Webhook egress guard is bypassed in this test only.** The production `Sender` posts through
  the SSRF egress choke, which blocks loopback **by design** (Slice 05, unit-tested). The sink
  here is `127.0.0.1`, so the E2E uses a plain client to exercise the *delivery + signing +
  tenant-binding* path; the egress guard is not what this test covers.
- **G2/G4 ledgers are still in-memory.** The E2E proves the *engine* upholds them across the
  wired system, but their datastore durability (RLS-scoped Postgres tables) is a later slice —
  the composite store here binds G5→Postgres, G2/G4→memory.
- **Fixed clock + deterministic fakes** keep the assertions stable; this is a correctness
  test, not a load/latency test (that is docs/21's performance track).
- The HTTP layer is driven with `httptest` recorders (in-process handler), not a bound
  socket; the webhook sink *is* a real `httptest.Server`. This exercises the full handler
  chain without port management.

## 4. Reviewer result
| Check | Result |
|-------|--------|
| Real JWT → API → queue → engine → **live PG** → webhook, end to end | PASS (live) |
| G1 isolation enforced by the DATABASE across the HTTP boundary | PASS (live) |
| G2 no double paid call; G4 no overspend; G5 provenance intact | PASS (live) |
| Webhook delivered with a verifiable signature | PASS (live) |
| Mainline `go build/vet/test/gofmt` clean; integration DSN-gated | PASS |
| Webhook-guard bypass + in-memory G2/G4 disclosed honestly (§3) | PASS |

**Gate:** slice `IMPLEMENTED`; the system is proven working end-to-end against a real
database. Proceeds to the next increment on request.
