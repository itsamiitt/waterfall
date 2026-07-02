# 32 — Implementation Slice 10: Postgres store + live tenant-isolation (RLS) proof (Go)

**Status:** `IMPLEMENTED` (mainline green + **live RLS test passed on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`31`](31-Implementation-Slice-09.md) · **Canonical spec:** [`06`](06-Data-Model.md), [`18`](18-Security.md) §1 (G1), [`21`](21-Testing.md) §1 (release-blocker) · **Migration:** [`0001_init.sql`](../migrations/0001_init.sql) · **Approved by:** human (2026-07-01)

> Closes the biggest gap between the in-memory prototype and production: tenant isolation
> (gate **G1**) enforced by the **database** via row-level security, not just the application
> — and **proven live** against a real PostgreSQL, which docs/21 named the release-blocker.

## 1. Zero-dependency Postgres client — `internal/pg`
To keep the project's zero-external-dependency property, this slice does **not** pull a
driver; it implements the PostgreSQL wire protocol (v3) in stdlib:
- Startup handshake (trust / cleartext auth — the deployment terminates TLS + SCRAM at a
  proxy on a private network, docs/12; SCRAM in-client is future work).
- **Simple** query protocol (DDL, multi-statement migrations) and **extended** query protocol
  (`Parse`/`Bind`/`Execute`/`Sync`) with bound parameters — parameters, not string
  interpolation, are what keep the datastore injection-free.
- Text result decoding with NULL handling; `ErrorResponse` surfaced as a structured `PGError`;
  the connection stays usable after a query error (`Sync` recovery).

Verified live (`TestConn_SimpleAndExtended`): simple + parameterized round-trips, NULLs, error
recovery — all against PostgreSQL 17.10.

## 2. Postgres FieldVersions store — `internal/pgstore`
Implements the `store.FieldVersions` contract (gate G5) over `internal/pg`. The isolation
discipline is identical to the in-memory store: **tenant id is read from the request principal,
never an argument.** Each operation runs in a transaction that binds
`SET LOCAL app.current_tenant = <principal tenant>` (via `set_config(..., is_local => true)`),
and every RLS policy scopes rows to that GUC. `Append` writes `tenant_id =
current_setting('app.current_tenant')`, so the RLS `WITH CHECK` guarantees a row can only land
in the caller's own partition. Fails **closed**: no principal ⇒ `ErrNoPrincipal`.

## 3. The live RLS proof — `internal/pgstore` (build tag `integration`)
The migration ([`0001_init.sql`](../migrations/0001_init.sql), already authored) uses
`FORCE ROW LEVEL SECURITY` + `USING`/`WITH CHECK (tenant_id = app_current_tenant())`. The test
builds the schema as superuser, then runs **every assertion as a non-superuser role
`app_rls`** (superusers and `BYPASSRLS` roles are exempt from RLS — testing as one proves
nothing). It asserts:

| # | Assertion | Result |
|---|-----------|--------|
| 1 | As tenant-A, `count(*)` over a table holding an A row and a B row is **1**; B's row invisible (and symmetrically for B) | PASS |
| 2 | As tenant-A, an INSERT stamped `tenant-B` is **rejected** by `WITH CHECK` | PASS |
| 3 | `pgstore` under a tenant-A principal writes a value tenant-A reads back; a tenant-B principal sees **nothing** | PASS |
| 4 | `Append` with **no principal** in context is rejected (fail-closed) | PASS |

This is the docs/21 §1 release-blocker: *cross-tenant read = 0 rows*, now demonstrated on a
real database. Reproducible via [`scripts/run-rls-test.sh`](../scripts/run-rls-test.sh) (uses
`WATERFALL_PG_DSN` if set, else spins an ephemeral trust cluster).

## 4. Environment note (honest)
There was no Docker/psql on PATH; a PostgreSQL 17 install was present. The live run used an
**ephemeral trust cluster** on `127.0.0.1:55432` (a fresh `initdb`, held for the test, torn
down after). A Windows-specific wrinkle — postgres backends spawned under `pg_ctl`'s
admin-dropping restricted token crash with `0xC0000142` only when the parent is killed
mid-init; a normally-running server is unaffected. CI (Linux) uses a Postgres service
container via the same script.

## 5. Honestly out of this slice
- **Only `FieldVersions` (the G5 table) is ported.** `IdempotencyLedger` (G2) and `CostLedger`
  (G4) follow the identical RLS pattern and are not yet implemented on Postgres — the engine
  still uses the in-memory ledgers.
- **No connection pool** — the store serializes one connection with a mutex (correct for the
  proof; production fronts it with a pool that sets the tenant GUC per checked-out conn).
- **No SCRAM/MD5 or TLS in the client** — trust/cleartext only; the private-network + proxy
  posture (docs/12) is assumed. SCRAM (feasible with Go 1.26 `crypto/pbkdf2`) is future work.
- **No migration runner / versioning** — `0001_init.sql` is applied directly by the test; a
  real migration tool (up/down, schema_migrations table) is deferred.
- **No prepared-statement caching / pipelining / binary format** — one statement at a time.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| RLS blocks cross-tenant **read** (0 rows) — release-blocker | PASS (live) |
| RLS `WITH CHECK` blocks cross-tenant **write** | PASS (live) |
| App store enforces isolation via principal; fail-closed w/o principal | PASS (live) |
| Assertions run as a **non-superuser** (RLS actually applies) | PASS |
| Parameterized queries (no SQL injection); error-recovery on conn | PASS |
| Zero new external dependencies (stdlib pgwire client) | PASS |
| `go build/vet/test/gofmt` clean (mainline); reproducible harness | PASS |
| G2/G4 tables, pooling, SCRAM/TLS, migration runner deferred (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; the G1 datastore release-blocker is **satisfied and
live-verified**. Proceeds to the next increment on request.
