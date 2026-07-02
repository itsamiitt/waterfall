# 34 — Implementation Slice 12: Postgres G2/G4 ledgers + connection pool (Go)

**Status:** `IMPLEMENTED` (mainline green + **live ledger + E2E tests passed on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`33`](33-Implementation-Slice-11.md) · **Canonical spec:** [`06`](06-Data-Model.md), [`skills/waterfall-correctness`](../skills/waterfall-correctness/SKILL.md) (G2/G4) · **Approved by:** human (2026-07-01)

> Ports the last two ledgers to the database, so **every** correctness gate is now enforced
> at the datastore with RLS — not just G5. `pgstore` is now a full `store.Store`, backed by a
> bounded connection pool, and the live E2E runs entirely on Postgres (nothing in memory).

## 1. Idempotency ledger — G2 (`internal/pgstore`)
- `Record`: `INSERT ... ON CONFLICT (tenant_id, idempotency_key) DO NOTHING` — **first writer
  wins**, so concurrent retries converge on one paid result. The `provider.Result` is stored
  as `jsonb`.
- `Lookup`: `SELECT result WHERE idempotency_key = $1`, JSON-decoded back to `provider.Result`.
- RLS scopes both to the tenant GUC, so a tenant can neither read nor forge another's ledger
  entry. Verified live: round-trip, cross-tenant invisibility, and first-writer-wins (a second
  `Record` with a different payload does **not** overwrite).

## 2. Cost ledger — G4 (`internal/pgstore`)
The reservation is a **single guarded UPDATE** — the atomic heart of the ceiling:
```sql
UPDATE cost_ledger SET committed = committed + $amount
 WHERE tenant_id = current_setting('app.current_tenant') AND job_id = $job
   AND committed + $amount <= $ceiling
RETURNING committed;
```
Zero rows returned ⇒ the add would breach the ceiling ⇒ `store.ErrCeilingExceeded`, no change.
A row lock serializes concurrent reservations, so the ceiling holds even under contention (no
read-modify-write race). `Release` refunds with `GREATEST(0, committed - $amount)`.
Verified live: reserve within ceiling, **rejection leaves committed unchanged**, reserve to the
ceiling, release, and cross-tenant isolation (tenant-B sees 0 for tenant-A's job).

## 3. Connection pool — `internal/pg`
A bounded pool (`Pool`): a token caps **open** connections at `max`; an idle connection keeps
its token (open for reuse); closing one returns its token. `Get(ctx)` reuses an idle
connection, opens a new one under the cap, or blocks (honoring `ctx`) when saturated. Each
`pgstore` op checks out a connection, runs **one transaction** that binds the tenant GUC
`SET LOCAL`, then returns it — so a pooled connection is never shared across tenants
mid-transaction. Unit-tested (mainline, injectable dialer): cap bounding, reuse-without-dial,
and broken-connection eviction.

## 4. Full-stack E2E now entirely on Postgres
The Slice-11 E2E previously bound G2/G4 to memory; it now uses **`pgstore` as the full
`store.Store`**. All gates are datastore-durable, and the test additionally asserts (as
superuser) that the `idempotency_ledger` and `cost_ledger` tables are **non-empty** after the
run — proving the engine's G2/G4 effects actually persisted to Postgres, not just to process
memory.

## 5. Tests
Mainline: `TestPool_BoundsAndReuse`. Live (`-tags integration`, DSN-gated):
`TestPG_IdempotencyLedger`, `TestPG_CostLedger`, plus the existing RLS + full-stack E2E —
**5 live integration tests, all passing** on PostgreSQL 17.10. Mainline suite (89 tests)
`go build/vet/test/gofmt` clean.

## 6. Honestly out of this slice
- **No SCRAM/TLS in the client** (trust/cleartext only) and **no migration runner** — carried
  over from Slice 10.
- **The pool has no health-check/max-lifetime/idle-reaper** — a connection broken *between*
  uses is only detected on next use (marked broken, evicted). Fine for the workload; a
  production pool would add liveness pings + recycling.
- **`Reserve` seeds the row then updates in the same transaction** — correct, but two
  statements; a single `INSERT ... ON CONFLICT DO UPDATE ... WHERE` with a first-insert ceiling
  guard could fold it to one round-trip (micro-optimization, not done).
- **The durable outbox (Slice 03) is still the file-WAL**, not a Postgres outbox in the same
  transaction as these writes — that is the next natural increment.

## 7. Reviewer result
| Check | Result |
|-------|--------|
| G2 first-writer-wins + tenant isolation (live) | PASS |
| G4 atomic guarded reservation; rejection leaves state unchanged; isolation (live) | PASS |
| `pgstore` is a full `store.Store`; E2E runs entirely on Postgres | PASS |
| Pool bounds open conns, reuses, evicts broken (unit-tested) | PASS |
| Per-transaction tenant GUC — no cross-tenant conn sharing | PASS |
| Mainline `go build/vet/test/gofmt` clean; 5 live integration tests pass | PASS |
| SCRAM/TLS, migration runner, pool liveness, PG outbox deferred (§6) | PASS |

**Gate:** slice `IMPLEMENTED`; **all five correctness gates are now datastore-enforced with
RLS and live-verified.** Proceeds to the next increment on request.
