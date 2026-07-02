# 25 — Implementation Slice 03: durable queue + transactional outbox (Go)

**Status:** `IMPLEMENTED` (tests green + live restart-recovery) · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`24`](24-Implementation-Slice-02.md) · **Approved by:** human (2026-07-01)

> The third increment makes async jobs **crash-safe**: a job accepted by the API survives a
> process crash and is re-driven to completion, with no double charge. This is the
> hand-rolled-saga + outbox fallback path (ADR-0013/0014, docs/10 §4) made concrete and
> dependency-free.

## 1. What this slice adds
- **Write-ahead Log** (`internal/durable/log.go`): append-only, `fsync`'d, framed
  (`len | CRC32 | JSON`) records with **atomic commit-marked batches** and **torn-tail
  recovery** — a crash mid-append drops the whole partial batch, never half of it.
- **Durable Store + transactional outbox** (`internal/durable/store.go`): submitting a job
  atomically appends the **job snapshot + an outbox publish-intent** in one committed
  batch (no dual-write gap). The intent is cleared **only** when the job is durably
  terminal, in the same atomic batch — so execution (not just publish) is crash-safe.
  Implements `job.Store` **and** `job.Submitter`; tenant-scoped (G1).
- **Relay** (`internal/durable/relay.go`): moves pending intents onto the in-process queue;
  on startup it re-drives every non-terminal recovered job (**at-least-once**).
- **Submitter seam** (`internal/job/submitter.go`): the API now depends on a `Submitter`
  interface — in-process `QueueSubmitter` (Slice-02 behaviour) or the durable store —
  selected at wiring. `cmd/enrichapi` picks durable when `DURABLE_LOG` is set.

## 2. Why redelivery is safe (the crux)
At-least-once means a recovered job may run again. That is safe **because the engine is
idempotent (G2)**: the re-run's provider call hits the idempotency ledger and returns the
stored result with **no second paid call**. Durability (this slice) and idempotency
(Slice-01) compose into effectively-exactly-once **charging**.

## 3. Tests (5 new; 47 total)
`log`: append/replay round-trip, **torn-tail recovery** (garbage tail dropped, log still
appendable), **uncommitted-batch dropped** (atomic batches). `store`/pipeline:
**crash recovers jobs + outbox**, cross-tenant recovered-job read = not found (G1), and the
payoff — **`TestPipeline_CrashRecoveryExactlyOnceCharge`**: 3 jobs submitted then "crashed"
before processing are recovered and completed, a forced **duplicate delivery causes 0 extra
provider calls**, and the outbox ends empty.

## 4. Live proof (verified over HTTP)
Ran `enrichapi` with `DURABLE_LOG` set → submitted an async job → succeeded (email fused,
8 credits). **Killed the process**, confirmed the WAL on disk (2,520 bytes), **restarted a
fresh process on the same log**, and `GET /v1/enrichments/{id}` returned the **recovered
succeeded outcome** — reconstructed entirely from the WAL.

## 5. Honestly out of this slice
- **Distributed** durability: this is a single-node file WAL. Production uses Kafka/Redpanda
  (ADR-0013) behind the same append/replay contract + the DB outbox/CDC of docs/10 §4.
- Enrichment **field data** is still the in-memory engine store (Slice-01) — only **job**
  state is durable here; the Postgres store (Slice-01 §5) makes field data durable + RLS.
- Log compaction/segment rotation (the WAL grows unbounded); consumer-group offsets.
- fsync is per-append (durable but not throughput-tuned); batching/group-commit is later.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Job accepted by API survives a crash (live-verified) | PASS |
| Outbox is transactional (atomic job+intent); no dual-write gap | PASS |
| Execution crash-safe (intent cleared only on terminal) | PASS |
| At-least-once redelivery → no double charge (G2) | PASS |
| Torn-tail / uncommitted-batch recovery | PASS |
| Tenant isolation preserved on recovered jobs (G1) | PASS |
| `go build/vet/test/gofmt` clean | PASS |
| Deferred scope logged, not hidden (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; proceeds to the next increment on request.
