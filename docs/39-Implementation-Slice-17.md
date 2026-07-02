# 39 — Implementation Slice 17: outbox dead-letter queue + max-attempts (Go)

**Status:** `IMPLEMENTED` (mainline green + **DLQ path proven live on PostgreSQL 17; crash-recovery still passes**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`38`](38-Implementation-Slice-16.md), [`35`](35-Implementation-Slice-13.md) (outbox) · **Canonical spec:** [`08`](08-Reliability.md), [`10`](10-Async-and-Queueing.md) · **Approved by:** human (2026-07-01)

> Closes the reliability gap flagged across Slices 13/16: the at-least-once outbox redelivered a
> failing job **forever**. Now a job that never reaches a terminal state after N deliveries is
> **parked in a dead-letter queue** — it stops consuming a worker, emits a metric, and surfaces
> in a tenant-scoped admin read — instead of looping.

## 1. The poison-job condition (why this is needed)
A job that RUNS and returns an error becomes `failed`, which is **terminal** — the outbox clears
`pending` and never redelivers it. So terminal status already handles the ordinary failure. The
gap is the **crash loop**: a job whose worker dies before any terminal `Put` (kill/OOM/panic that
takes the process) stays `pending`, is re-claimed after the visibility timeout, kills the worker
again… forever. Max-attempts backstops exactly this case, which status alone cannot catch.

## 2. Mechanism
- **Migration `0003_outbox_dlq.sql`** adds `attempts int`, `dead boolean`, `last_error text` to
  `job_outbox`, plus a partial index on dead rows for cheap DLQ listing.
- **`Relay.claim` (rewritten)** now increments `attempts` on every claim inside the same atomic
  `UPDATE … FOR UPDATE SKIP LOCKED`. A claim whose new attempt count would exceed `maxAttempts`
  sets `dead=true, pending=false, last_error='dead-lettered after N …'` and is **not** delivered.
  A parked row has `pending=false`, so it is never scanned again.
- **`NewRelay(…, WithMaxAttempts(n), WithDeadLetterHook(fn))`** — default 10; the hook fires once
  per parked row (for a metric/alert), on the drain goroutine.
- **Tenant-scoped read** `Store.DeadLetters(ctx, limit)` (RLS-scoped) + **`GET /v1/dead-letters`**
  (registered only when a lister is wired), so a tenant can inspect its own parked jobs.
- **`cmd/enrichapi`** wires `OUTBOX_MAX_ATTEMPTS` (default 10), the `outbox_dead_letter_total`
  counter + a `Warn` log on the hook, and the DLQ endpoint via a small adapter (keeps `api` and
  `pgoutbox` decoupled).

## 3. Live proof — `TestPGOutbox_DeadLetterAfterMaxAttempts` (PostgreSQL 17)
A poison job is submitted but never acked (no terminal `Put`), then drained repeatedly with a 1ms
visibility. Asserted, all live: after `maxAttempts` (3) deliveries the **next claim parks it**;
the hook fires **exactly once**; the tenant-scoped `DeadLetters` read returns it with
`attempts > max` and a non-empty `last_error`; **further drains do not re-claim it**; and
**tenant-B does not see tenant-A's dead letters** (G1). The Slice-16 crash-recovery harness still
passes unchanged (2 pending at crash → 40/40 recovered, 40 ledger rows) — the attempt counter does
not disturb the normal recovery path (a recovered job acks and stops at attempts=1–2, well under
the cap).

## 4. Honestly out of this slice
- **No automatic retry/replay of a dead-lettered job.** The DLQ is inspect-only via the API; a
  redrive endpoint (reset `dead/attempts` after a fix) is future work.
- **`attempts` counts DELIVERIES, not just failures.** A job that legitimately runs longer than
  the visibility timeout is re-claimed (and re-counted); operators must set visibility above the
  expected processing time. Max-attempts is a backstop for never-terminating jobs, not a retry
  budget for slow ones — documented, not enforced.
- **No alert routing** beyond the counter + log; hooking `outbox_dead_letter_total` to an alert is
  an ops config, not code.
- **The DLQ endpoint is tenant-scoped, not an operator-wide view.** A cross-tenant ops console
  (BYPASSRLS read) is not added.

## 5. Reviewer result
| Check | Result |
|-------|--------|
| Poison job parked after max attempts (dead=true, pending=false) | PASS (live) |
| Parked row is never re-claimed / re-dead-lettered | PASS (live) |
| Dead-letter hook fires exactly once per parked row | PASS (live) |
| Tenant-scoped DLQ read returns it with attempts + last_error | PASS (live) |
| G1: another tenant cannot see the dead letters | PASS (live) |
| Normal crash-recovery path unaffected (Slice-16 harness) | PASS (live) |
| Migration 0003 ordered/idempotent; runner test updated | PASS (live) |
| Mainline `go build/vet/test/gofmt` clean (94 tests) | PASS |
| Redrive/replay, slow-job vs visibility, alerting honestly scoped (§4) | PASS |

**Gate:** slice `IMPLEMENTED`; the durable queue can no longer be jammed by a single poison job.
Proceeds to the next increment on request.
