# 35 — Implementation Slice 13: Postgres transactional-outbox durable queue (Go)

**Status:** `IMPLEMENTED` (mainline green + **live crash-safety test passed on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`34`](34-Implementation-Slice-12.md) · **Canonical spec:** [`10`](10-Reliability.md) §4 (transactional outbox), [`03`](../adr/0012-idempotency-key-required.md) · **Approved by:** human (2026-07-01)

> Replaces the file-WAL durable queue (Slice 03) with a **Postgres transactional outbox** —
> the production durability story: a submitted job is durably captured as a row, a relay
> claims pending rows with `FOR UPDATE SKIP LOCKED` (competing consumers), and a crash before
> the durable-terminal ack leaves the row re-drivable (at-least-once). The engine's G2
> idempotency makes redelivery **exactly-once-effective**.

## 1. Schema — `migrations/0002_job_outbox.sql`
One row per job: `payload jsonb` (the serialized `job.Job`, carrying the principal), `status`,
`pending boolean` (the **outbox intent**: true until durably terminal), `claimed_at` (drives the
visibility timeout). A partial index over pending rows keeps the relay's scan cheap. RLS +
`FORCE ROW LEVEL SECURITY` scope **tenant** access; the relay is a trusted system consumer on a
BYPASSRLS connection (each row keeps its `tenant_id`, which flows into execution via the job's
captured principal — G1).

## 2. Store — `internal/pgoutbox.Store` (drop-in `job.Store` + `job.Submitter`)
Mirrors the file-WAL semantics exactly, on Postgres:
- **`Submit`**: `INSERT ... (pending=true) ON CONFLICT (job_id) DO NOTHING` — durable capture
  before any async work; idempotent; never sheds.
- **`Put`**: upsert the snapshot; `pending` is cleared **iff terminal, in the same UPDATE** as
  the snapshot — so a crash before durable-terminal leaves the row re-drivable.
- **`Get`**: RLS-scoped read (another tenant's job reads as not-found).

All three set the tenant GUC from the principal (G1); a `TenantID != principal` submit is
rejected (`ErrTenantMismatch`).

## 3. Relay — `internal/pgoutbox.Relay`
The "message relay" half. It claims pending rows atomically:
```sql
UPDATE job_outbox o SET claimed_at = now() FROM (
  SELECT job_id FROM job_outbox
   WHERE pending AND (claimed_at IS NULL OR claimed_at < now() - make_interval(secs => $visibility))
   ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $batch
) sel WHERE o.job_id = sel.job_id RETURNING o.payload;
```
- **`FOR UPDATE SKIP LOCKED`** → multiple relay replicas poll concurrently without
  double-claiming (competing consumers).
- **Visibility timeout** → a claimed-but-unfinished row is only re-claimable once stale, which
  is exactly how a crashed relay's in-flight jobs recover.
- `Start` drains immediately (startup recovery) then on an interval.

## 4. Live crash-safety proof — `internal/pgoutbox` (`-tags integration`)
`TestPGOutbox_DurableDeliveryAndCrashSafety` (passed on PostgreSQL 17.10) asserts:
| Scenario | Result |
|----------|--------|
| Normal: submit → claim → worker → terminal; provider called once; **outcome round-trips through JSONB**; completed job not re-claimed | PASS |
| **Crash + redelivery**: reset the row to pending → relay re-claims → re-run → **0 new provider calls** (G2 exactly-once-effective) | PASS |
| Visibility: a freshly-claimed in-flight row is **skipped**; a stale claim is **recovered** | PASS |
| Tenant isolation (RLS) on reads; `ErrTenantMismatch` on a cross-tenant submit | PASS |

## 5. Honestly out of this slice
- **Not wired into `cmd/enrichapi`.** Production wiring needs two roles — a tenant-scoped app
  role (Store) and a **BYPASSRLS relay role** (Relay) — provisioned by ops/migrations; the demo
  command keeps the file-WAL path to avoid a misleading single-superuser setup. This is a
  config/provisioning step, not new logic.
- **The outbox row is not co-located in the same transaction as the engine's field/ledger
  writes.** Those still commit across several `pgstore` transactions during the waterfall;
  co-locating an event write would require running a whole job in one DB transaction (an engine
  transaction-model change). This slice delivers the transactional outbox for *durable job
  delivery* (the Slice-03 replacement), which is the load-bearing use.
- **No dead-letter / max-attempts / poison-pill handling** — a permanently failing job would be
  re-claimed after each visibility timeout forever; a retry budget + DLQ is future work.
- **Relay uses a single connection** from its own loop (serialized); horizontal scaling is by
  running more relay processes (SKIP LOCKED makes that safe), not more connections per relay.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Durable capture (Submit) + terminal-clears-pending-atomically (Put) | PASS |
| `FOR UPDATE SKIP LOCKED` competing-consumers claim | PASS |
| Crash → at-least-once redelivery; G2 → exactly-once-effective (live) | PASS |
| Visibility-timeout recovery of in-flight claims (live) | PASS |
| Tenant isolation on reads; cross-tenant submit rejected | PASS |
| Mainline `go build/vet/test/gofmt` clean; 6 live integration tests pass | PASS |
| cmd wiring, same-txn event outbox, DLQ honestly deferred (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; the durable queue is now a real Postgres transactional outbox,
crash-safety live-verified. Proceeds to the next increment on request.
