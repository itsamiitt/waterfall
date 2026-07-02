# 23 — Implementation Slice 01: correctness-gate vertical slice (Go)

**Status:** `IMPLEMENTED` (tests green) · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Language:** Go 1.26 · **Approved by:** Planning Completion Gate + human (2026-07-01)

> The first implementation increment: a thin, end-to-end enrichment path that makes all
> **five correctness gates provable** in code, before any breadth. "Model proposes,
> deterministic gate disposes" is realised concretely here.

## 1. What this slice does
Enrich one Subject for a set of Fields through ≥1 Provider: the **Router** proposes a
cost-ordered plan, the **Execution Engine** runs it while enforcing G1–G5 around every
call, confidences are **fused** (log-odds) with a **sequential stop**, and every value is
persisted with **provenance**. Runnable demo: `go run ./cmd/enrichd`.

## 2. Package map (`internal/`)
| Package | Responsibility | Docs |
|---------|----------------|------|
| `domain` | Canonical Field vocab, Confidence, Credits, Provenance, EnrichmentRequest, **canonical idempotency-key derivation**, error taxonomy | `00` §7, `05`, skills |
| `tenant` | Principal binding — the **only** source of `tenant_id` (G1) | `04` §4, `18` §1 |
| `provider` | Adapter contract (secret-free, emits auth descriptor), circuit **Breaker**, **bounded Call** (G3), generic **HTTPAdapter** (API-first) + status→taxonomy | `05`, `09`, `13`, skills/api-integration |
| `router` | Reservation-value ordered **Plan** (Pandora-lite; proposes only) | `08`, ADR-0007/0008 |
| `engine` | Execution spine enforcing G1–G5; log-odds **fusion** + SPRT-lite stop; charge-on-success | `09`, ADR-0005/0006 |
| `store` | Tenant-scoped repositories: idempotency (G2), cost (G4), field_versions (G5); in-memory impl | `06` |
| `cmd/enrichd` | Runnable demo wiring the slice | — |
| `migrations/0001_init.sql` | Canonical Postgres **FORCE RLS** DDL (real G1 datastore enforcement) | `06`, `18` |

## 3. Gate → enforcement → test (the proof)
| Gate | Enforced in code | Proven by test |
|------|------------------|----------------|
| **G1** tenant isolation | `tenant` principal is the only tenant source; every `store` op scopes by it; fails closed with no principal | `store.TestG1_CrossTenantIsolation` (release-blocker), `TestG1_NoPrincipalFailsClosed`, `engine.TestG1_TenantScopedResults` |
| **G2** idempotency | ledger `Lookup` before any paid call; `Record` terminal result; key = `hash(tenant,record,field,provider,params,config_version)` | `engine.TestG2_ReplayNoDoubleChargeOrCall` (1 call + 1 charge across 2 runs) |
| **G3** bounded | `provider.Call`: per-attempt timeout, capped bounded retries (retryable classes only), circuit breaker | `provider.TestG3_TimeoutBoundsACall`, `TestG3_RetriesAreBounded`, `TestG3_BreakerOpensAndRecovers` |
| **G4** cost ceiling | `Reserve` **before** each paid call; can never exceed ceiling; charge-on-success `Release` refunds failed/empty | `store.TestG4_ReserveNeverExceedsCeiling`, `engine.TestG4_CeilingStopsSpending` |
| **G5** provenance | `FieldValue.Valid()` + store `Append` reject bare values; every write carries Provider+key+time+cost | `store.TestG5_RejectBareValue`, `engine.TestG5_HappyPathRecordsProvenance` |
| SSRF (P2) | egress-injection seam modelled: adapters are secret-free; HTTPAdapter sets header *name* only | (network-policy + resolver are `13`; egress-proxy is a later slice) |

Also: `router.TestPlan_*` (ordering/determinism/validation), `provider.TestHTTPAdapter_*`
(API-first success + full status taxonomy + ctx timeout), fusion early-stop, failover.

## 4. How to build / test / run
```
go build ./...
go test ./...            # all gate tests; add -race on a host with a C toolchain
go test -cover ./...     # store 79% · engine 78% · tenant 89% · router 72% · provider 68%
go run ./cmd/enrichd     # live waterfall demo with provenance + G2 replay
```

## 5. Honestly out of this slice (next increments, not hidden)
- **Postgres-backed store + RLS integration test** — DDL exists (`migrations/0001`); the
  live `//go:build integration` test needs a database + pgx driver (`go get`), run in CI
  as the G1 release-blocker (`21`). The in-memory store proves the *application* contract now.
- **Egress-proxy** (the real SSRF choke + key injection, `13`) — modelled as a seam here.
- **Queue/async jobs, API gateway, Temporal orchestration** (`07`/`10`/`14`) — later slices.
- **Real provider adapters** — HTTPAdapter is the template; concrete vendors from `03`.
- **Calibration** (isotonic/Platt, ADR-0005) — fusion assumes calibrated inputs for now.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| All five gates enforced in code + each proven by a test | PASS |
| `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l` clean | PASS |
| Idempotency key matches canonical form (`04`/`09`/skill) | PASS |
| No secret in adapter memory (egress-injection seam) | PASS |
| Deferred scope logged, not hidden (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; proceeds to the next increment on request.
