# 42 — Implementation Slice 20: config validation + startup self-check (Go)

**Status:** `IMPLEMENTED` (mainline green + **fail-fast + G1 self-check proven live on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`41`](41-Implementation-Slice-19.md), [`38`](38-Implementation-Slice-16.md) · **Canonical spec:** [`18`](18-Security.md), [`20`](20-Monitoring.md) · **Approved by:** human (2026-07-01)

> Turns misconfiguration from a per-request surprise into a loud startup failure. The service now
> validates its whole config up front (reporting every problem at once), verifies at boot that it
> connects as a role that **cannot** bypass tenant isolation and that the schema is migrated, and
> exposes a real `/readyz` that is green only when the datastore is reachable.

## 1. Config validation — `internal/config`
`config.Load(getenv)` (pure: env in, `Config` + joined error out — fully unit-testable) validates:
- **PORT** parses to 1..65535; **OUTBOX_MAX_ATTEMPTS** is an integer ≥ 1; **JWT_HS256_SECRET**, if
  set, is ≥ 16 bytes.
- **DSNs** (`POSTGRES_DSN` / `_ADMIN_DSN` / `_RELAY_DSN`) include `user=` and `dbname=`.
- **Coherence**: an admin/relay DSN without a primary DSN is rejected; `POSTGRES_DSN` and
  `DURABLE_LOG` are mutually exclusive.
It returns **all** problems joined (not just the first), and `main` logs them and exits. `main`
was refactored to read every setting from the validated `Config` instead of scattered
`os.Getenv`.

## 2. Startup self-check — `cmd/enrichapi` `startupSelfCheck`
When Postgres is enabled, before serving, over a fresh app-role connection:
1. **G1 safety** — `pg.Conn.RolePrivileges()` reports whether the role is a superuser or has
   BYPASSRLS; if so the app **refuses to start** (such a role silently defeats RLS — the most
   dangerous misconfiguration for a multi-tenant system).
2. **Schema present** — the tables the app needs (`field_versions`, `idempotency_ledger`,
   `cost_ledger`, `job_outbox`) must exist; otherwise it exits telling you to run migrations.
`pgmigrate.Pending(conn, dir)` was added as the precise migration-drift primitive (returns the
`*.sql` files not yet recorded in `schema_migrations`).

## 3. Readiness probe — `GET /readyz`
Distinct from `/healthz` (liveness). Returns `200 {"status":"ready"}` only when the optional
`ReadyCheck` passes; `main` wires it to `pgstore.Store.Ping` (a pooled `select 1`), so a load
balancer routes traffic to an instance only once its datastore is actually reachable. In memory
mode there is no check and it is always ready. Declared in the OpenAPI spec (200 / 503).

## 4. Live proof (PostgreSQL 17)
- **`TestRolePrivileges`** (internal/pg): a superuser reports super=true; a `nosuperuser` role
  reports both false (the required state); a `BYPASSRLS` role is detected.
- **`TestPending_ReportsUnapplied`** (internal/pgmigrate): a virgin DB reports all migrations
  pending; after `Apply`, none.
- **Binary, observed:** bad config → logs all three errors and refuses to start; a **superuser
  app DSN → refuses to start** with the G1 message (`superuser=true, bypassrls=true`); memory-mode
  `/readyz` → `{"status":"ready"}`; and the Slice-16 crash-recovery harness still passes (happy-path
  self-check as the `app_rls` role, 40/40).
- **Unit:** `config` (4 tests incl. all-errors aggregation + coherence) and `/readyz` (200/200/503).
  Mainline now 99 tests, `go build/vet/test/gofmt` clean.

## 5. Honestly out of this slice
- **`/readyz` checks only the primary datastore reachability** — not the relay connection, the
  webhook targets, or downstream vendors. Adequate for load-balancer routing; deeper dependency
  health is future work.
- **The self-check runs once at startup**, not continuously — a role altered at runtime isn't
  re-detected (roles don't normally change under a running process).
- **No config file / flags** — environment only (12-factor); a file loader is not added.
- **Webhook config is still read directly in `main`** (not centralized), as it is optional and
  tenant-scoped rather than a startup-blocking concern.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Config validated up front; ALL errors reported; bad config refuses to start | PASS (live) |
| App refuses to start as a role that bypasses RLS (G1 safety) | PASS (live) |
| Missing schema is detected at boot with an actionable error | PASS |
| `pgmigrate.Pending` reports drift; `RolePrivileges` detects bypass | PASS (live) |
| `/readyz` green only when the datastore pings; declared in OpenAPI | PASS (live + unit) |
| Happy-path startup unaffected (crash harness 40/40) | PASS (live) |
| Mainline `go build/vet/test/gofmt` clean (99 tests) | PASS |
| Continuous health, config files, relay/vendor readiness honestly scoped (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; a misconfigured or RLS-unsafe deploy now fails loudly at startup
instead of silently at request time. Proceeds to the next increment on request.
