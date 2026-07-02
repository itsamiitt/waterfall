# 40 — Implementation Slice 18: DLQ redrive / replay (Go)

**Status:** `IMPLEMENTED` (mainline green + **redrive→replay proven live on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`39`](39-Implementation-Slice-17.md) (DLQ) · **Canonical spec:** [`08`](08-Reliability.md), [`10`](10-Async-and-Queueing.md) · **Approved by:** human (2026-07-01)

> Closes the "inspect-only DLQ" gap from Slice 17: an operator can now **redrive** a parked job —
> reset it so the outbox re-delivers it — once the underlying bug is fixed. Tenant-scoped,
> write-scoped, audit-logged, metered, and proven end-to-end: park → redrive → the (now-working)
> worker completes it.

## 1. Mechanism
- **`Store.Redrive(ctx, jobID) (bool, error)`** — one RLS-scoped `UPDATE … WHERE job_id=$1 AND
  dead` that resets `dead=false, pending=true, attempts=0, claimed_at=null, last_error=null,
  status='queued'` and returns whether a row was reset. A tenant can only redrive its **own**
  parked job (RLS); a non-dead / unknown / other-tenant id returns `false`. The **payload is
  untouched**, so the same job re-executes — and G2 idempotency makes any partial prior effect
  safe.
- **`POST /v1/dead-letters/{id}/redrive`** — a **write**: gated on the same write scope as submit
  (403 without it), tenant from the principal (G1), `404` when there's nothing dead to redrive,
  `200 {job_id, status:"redriven"}` on success. Every redrive is **audit-logged**
  (`dlq_redrive` with tenant + user + job) and counted (`dlq_redrive_total`).
- **`cmd`** exposes it via the same decoupling adapter; the `DeadLetterLister` interface grew a
  `Redrive` method and became `DeadLetterAdmin`.

## 2. Live proof — `TestPGOutbox_RedriveReplaysParkedJob` (PostgreSQL 17)
End-to-end, all asserted live:
1. A poison job is parked (drained past `maxAttempts` with no worker) — confirmed in the DLQ.
2. **G1:** tenant-B's `Redrive` of tenant-A's job returns `false` (RLS).
3. Tenant-A's `Redrive` returns `true`; the job **leaves the DLQ**.
4. A now-working dispatcher + relay re-deliver it; it **runs to `succeeded`** with `work_email`
   filled.
5. A second `Redrive` of the now-completed job is a **no-op** (`false` — nothing dead to reset).

Writing this test surfaced (and the assertion caught) the slow-job-vs-visibility hazard from
Slice 17 §4: with a 1ms visibility the relay re-claimed the in-flight job and re-dead-lettered it
mid-run. The fix is operational, not code — the replay relay uses a visibility longer than the
worker time (as production must). Documented in the test.

## 3. Honestly out of this slice
- **Redrive resets `attempts` to 0.** A job redriven repeatedly without fixing its cause will
  re-park each cycle (bounded by max-attempts each time) — intended; redrive is a deliberate
  operator action, not automatic retry.
- **No bulk redrive** (redrive-all / by-filter) and **no scheduled/auto redrive** — one job per
  call, on purpose (blast-radius control).
- **Tenant-scoped only.** A cross-tenant operator redrive (BYPASSRLS) is not exposed.
- **The stale-`dead`-flag-after-late-success edge** (a row dead-lettered by an over-aggressive
  relay that then succeeds) is avoided by correct visibility, not prevented in SQL — same
  operational contract as Slice 17.

## 4. Reviewer result
| Check | Result |
|-------|--------|
| Redrive resets a parked job (dead→false, pending→true, attempts→0) | PASS (live) |
| RLS: a tenant cannot redrive another tenant's job (G1) | PASS (live) |
| Redriven job re-delivers and runs to `succeeded` | PASS (live) |
| Redrive of a non-dead / unknown job is a no-op (404 at the API) | PASS (live) |
| Endpoint is write-scoped + audit-logged + metered | PASS |
| OpenAPI declares the route (200/401/403/404) | PASS |
| Mainline `go build/vet/test/gofmt` clean (94 tests) | PASS |
| Bulk/auto/cross-tenant redrive + attempts-reset semantics honestly scoped (§3) | PASS |

**Gate:** slice `IMPLEMENTED`; the DLQ is now actionable — parked jobs can be recovered after a
fix, not just observed. Proceeds to the next increment on request.
