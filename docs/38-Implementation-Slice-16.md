# 38 — Implementation Slice 16: wire the full Postgres durable path into the binary (Go)

**Status:** `IMPLEMENTED` (mainline green + **live crash-recovery proven through the real binary on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`37`](37-Implementation-Slice-15.md), [`33`](33-Implementation-Slice-11.md) (pgstore), [`35`](35-Implementation-Slice-13.md) (outbox) · **Canonical spec:** [`08`](08-Reliability.md), [`13`](13-Datastore.md) · **Approved by:** human (2026-07-01)

> Everything built for Postgres over Slices 10–14 (RLS store, G2/G4 ledgers, transactional
> outbox, migration runner) is now wired into `cmd/enrichapi` behind env, and proven end-to-end:
> the **real binary**, hard-killed mid-flight, recovers every in-flight job from the outbox on
> restart. This is the production durable path, not a unit test.

## 1. What was wired — `cmd/enrichapi/main.go`
Datastore selection is now three-way, most-durable-first:
- **`POSTGRES_DSN` set → Postgres.** `pgstore` becomes the engine store + record store (RLS
  enforced), and `pgoutbox` becomes the job store + submitter. A `pgoutbox.Relay` on a privileged
  connection claims pending rows (`FOR UPDATE SKIP LOCKED`) into the in-process queue and
  **recovers in-flight jobs after a crash**.
- `DURABLE_LOG` set → single-node file-WAL outbox (unchanged, Slice 06).
- neither → in-process only.

**Self-bootstrapping (demo-grade), RLS-preserving (prod-grade).** When `POSTGRES_ADMIN_DSN` is
set, startup runs the migration runner (Slice 14) and idempotently provisions two
**non-superuser** roles — `app_rls` (RLS-enforced) and `relay` (`BYPASSRLS`, cross-tenant claim
only) — then the app connects as `app_rls` and the relay as `relay`. So a fresh cluster comes up
ready, yet tenant isolation (G1) is actually enforced at runtime because the app is *not* a
superuser/owner and cannot bypass the RLS policies. Job **execution** still runs under each job's
captured principal, so writes stay tenant-scoped even though the relay claims across tenants.

Short relay visibility timeout (3s) so a restarted relay re-claims jobs a dead process had
claimed-but-not-finished within seconds.

## 2. Live crash-recovery proof — `scripts/crash-recovery-test.sh`
Drives the **real compiled binary** against an ephemeral PostgreSQL 17 cluster:
submit 40 async jobs → **`kill -9` the process (a crash, not a graceful stop)** → restart → assert
every job completes. Observed run:

| Metric | Value |
|--------|-------|
| jobs submitted | 40 |
| durably captured in outbox before crash | 40 |
| **still pending at the moment of `kill -9`** | **3** (recovery genuinely exercised) |
| records present after restart | **40 / 40** |
| outbox rows delivered (terminal) | 40 |
| idempotency-ledger rows | **40** (G2: exactly one per job — no double-charge on redelivery) |
| still pending after recovery | 0 |

The 40-rows-for-40-jobs ledger count is the load-bearing assertion: the 3 redelivered jobs did
**not** execute twice. Result: **PASS**.

## 3. Honestly out of this slice
- **Role provisioning uses trust auth + a superuser admin DSN** in the demo. Production would
  provision `app_rls`/`relay` with SCRAM passwords (Slice 14) via a real ops step, not at app
  startup; the startup path is a convenience gated on `POSTGRES_ADMIN_DSN` being set.
- **The crash test is a shell harness, not a `go test`.** It kills and restarts an OS process, so
  it lives in `scripts/`, not the build-tagged Go suite. It requires a local PG + Go toolchain.
- **Single relay instance.** Multi-relay HA (competing consumers) is supported by the
  `SKIP LOCKED` + visibility design but is not exercised here.
- **No DLQ / max-attempts** on the outbox yet (a poison job would redeliver indefinitely) — still
  deferred (see Slice 13 §out-of-scope).
- **No graceful in-flight drain on SIGTERM into the outbox** — a graceful stop relies on the same
  recovery path as a crash, which the test shows works; a dedicated drain is future polish.

## 4. Reviewer result
| Check | Result |
|-------|--------|
| Postgres store + outbox + relay wired behind env; memory/file-WAL paths intact | PASS |
| App connects as a NON-superuser role → RLS (G1) enforced at runtime | PASS |
| Migrations + roles bootstrapped idempotently on startup (admin DSN) | PASS |
| Real binary recovers in-flight jobs after `kill -9` (3 pending → 40/40 done) | PASS |
| G2 holds across redelivery (40 ledger rows for 40 jobs, no double execution) | PASS |
| Mainline `go build/vet/test/gofmt` clean | PASS |
| Trust/superuser-bootstrap, shell-harness, single-relay, DLQ honestly scoped (§3) | PASS |

**Gate:** slice `IMPLEMENTED`; the production durable path runs end-to-end through the binary and
survives a real crash. Proceeds to the next increment on request.
