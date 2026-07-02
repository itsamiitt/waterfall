# 41 — Implementation Slice 19: consolidation — README, one-command demo, docs index (Go)

**Status:** `IMPLEMENTED` (demo runs end-to-end; mainline unaffected) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** all prior slices · **Canonical spec:** [`docs/README.md`](README.md), [`00`](00-Project-Overview.md) · **Approved by:** human (2026-07-01)

> A front door for the whole system: a top-level README, a single `scripts/demo.sh` that builds,
> tests, and demonstrates the engine + gateway (and the live DB harnesses when Postgres is
> present), and an updated docs index. No new engine code — this makes the 18 slices approachable
> and runnable by a newcomer. Writing the demo also caught and fixed a real latent test-harness
> race.

## 1. Deliverables
- **`README.md`** (new, top-level) — what it is, the five correctness gates (G1–G5) + the
  governing invariant, an architecture diagram, the stdlib-only property, a copy-pasteable
  quickstart, the full testing story (unit / live integration / crash harness), a repo map, and an
  explicit "what's honestly not here yet". Every claim is backed by code or a test.
- **`scripts/demo.sh`** (new) — one command, five phases: build → unit suite → offline engine demo
  (`enrichd`, prints provenance + G2 replay) → **live HTTP round-trip** against the gateway in
  memory mode (real JSON response + `/metrics` lines) → live PostgreSQL harnesses (auto-detected;
  skipped with a clear message if PG17 is absent). Runs fully offline by default.
- **`docs/README.md`** (updated) — the stale "awaiting approval to begin implementation" status is
  replaced with the real state (18 slices landed); added index rows for the implementation slices
  (`23`–`40`) and the top-level README.
- **godoc:** audited — every internal package already carries a package doc comment and both
  commands use the `// Command …` convention, so no changes were needed (verified, not assumed).

## 2. Bug found + fixed while building the demo
Running the demo's phase 5 exposed a **real latent race in `scripts/run-rls-test.sh`**: it runs
five integration packages against ONE shared database, and `go test` runs package binaries in
parallel — so `pgmigrate`'s drop/recreate raced `pgoutbox`'s schema setup, intermittently failing
`TestApply_OrderedAndIdempotent`. Fixed with **`-p 1`** (serialize package execution), with a
comment explaining why. Re-verified: all 9 harness tests pass deterministically, and the demo's
`run-rls-test.sh → crash-recovery-test.sh` chain (both on port 55432) tears down cleanly between
runs. This is exactly the kind of flake a consolidation pass is meant to surface.

## 3. Demo output (observed)
- Build ok (stdlib only); unit suite green.
- `enrichd`: fills `mobile_phone` (0.88, vendor-phone) and `work_email` (0.911, vendor-premium)
  under a 15-credit ceiling, 13 committed, and a replay showing **G2: no new paid calls**.
- HTTP round-trip: `POST /v1/enrichments?mode=sync` returns `succeeded` with `work_email` filled
  (0.72, vendor-cheap, 2 credits) and full provenance; `/metrics` shows `provider_calls_total`,
  `enrichment_fields_filled_total`, `http_requests_total`.
- Postgres phase: 9 live tests pass + crash-recovery 40/40.

## 4. Honestly out of this slice
- **No new engine/API behavior** — documentation + orchestration only (plus the harness bugfix).
- **The demo's Postgres phase needs a local PG17**; it degrades gracefully (skips with a message)
  rather than failing where PG is absent.
- **No architecture/onboarding video or hosted docs site** — Markdown + runnable scripts only.

## 5. Reviewer result
| Check | Result |
|-------|--------|
| Top-level README: accurate (every claim backed by code/test), no fabrication | PASS |
| `scripts/demo.sh` runs end-to-end (build → tests → engine → HTTP → PG) | PASS |
| Latent `run-rls-test.sh` parallel-DB race fixed (`-p 1`); 9 tests deterministic | PASS |
| Harness chain (run-rls → crash-recovery) tears down cleanly on shared port | PASS |
| Docs index updated; godoc coverage verified complete | PASS |
| Mainline unaffected (no Go source changed) | PASS |

**Gate:** slice `IMPLEMENTED`; the system now has a coherent front door and a single runnable demo.
Proceeds to the next increment on request.
