# Changelog

All notable changes to the planning + implementation of the Waterfall Enrichment Engine.
Format: reverse-chronological; group by phase; note back-propagated improvements explicitly.

## [Unreleased]

### 2026-07-06 — 200-provider rollout, Phase A (groundwork bridge) — ADR-0023
Built the bridge that makes real API-first adapters runnable at scale, ahead of the per-provider
waves (`Closo_Enrichment_Architecture_200_Tools`). **Field vocabulary** extended doc-first
(`docs/00 §7` then `internal/domain/field.go`, kept in lockstep): code caught up to the Glossary
(`naics`, `sic`, `technographics`, `intent_topics`, `funding_stage`) and added the L6–L8 firmo/intent
Fields (`company_revenue`, `company_founded_year`, `company_hq_country`, `company_hq_city`,
`company_type`, `company_linkedin_url`, `company_phone`, `duns_number`, `intent_score`,
`buying_signal`) — 18→33 canonical Fields, additive, no migration (`technographics`/`intent_topics`
stored as a single normalized comma-joined value). **Adapter registry**
(`internal/provider/adapters/registry.go`): append-only single source of truth; `All(client)` builds
the engine slice, `Hosts()` builds the egress allow-list; `TestRegistry_Invariants` enforces
Slug==NameV, `<slug>:default` selector prefix, canonical capability Fields, and https base URLs
(also fixed a latent `twilio-lookup` slug/selector mismatch). **Catalog seeder**
(`cmd/providerseed` + in-package `providers.Seed`): UPSERTs one `providers` row per registered
adapter from its introspected descriptor under `PlatformTx`; new rows land `op_state='disabled'`,
re-seeds refresh only the integration descriptor (operator lifecycle state preserved) — proven by
`seed_test.go`. **Binaries:** `cmd/enrichapi` now wires `adapters.All(egress)` through
`provider.NewEgressClient` with keys from `PROVIDER_KEYS` (or the rotation lease resolver in the
full platform); `cmd/enrichd` stays an offline demo but enumerates the registry. `go build ./...`
and `go test ./...` green.

### 2026-07-06 — Dashboard pending-OI closeout (post-P12 hardening waves)
Closed the open-items backlog after the P0–P12 build. Migration `0011` (mfa_used_steps,
dash_admin_idempotency, alert_rules.anomaly_floor_credits). **Security:** TOTP single-use replay
guard (VerifyAndConsume, login + step-up); durable admin idempotency ledger (replaces the in-process
map); fingerprint-pepper rotation; NIST SP800-38D AES-256-GCM KATs + PBKDF2-HMAC-SHA256 KATs;
X-Forwarded-For-spoof + session-fixation negatives; bulk session-revoke. **Telemetry:** live
rotation `Lease.Done` → usage_events feed (Config.RecordUsage). **Bulk jobs:** keys bulk-op/import on
the durable bulk_jobs lease model + an advisory-locked janitor that fails expired-lease jobs.
**Cost/alerts:** cost.anomaly added to the closed metric catalog + /meta/enums; per-rule anomaly
floor. **enrichd:** opt-in worker heartbeat with a minted HS256 machine JWT. **Contracts/tooling:**
openapi-admin.{json,yaml} + apispec parity test (145==145); pgmigrate `-- pgmigrate: no-transaction`
escape hatch; web `check:ci`. **Resilience:** configver test-only publish-crash fault hook +
PG-restart-reconnect + poison-import-row chaos tests; 50k-import and 1M-fold measured single-instance.
**Live E2E:** Playwright login→MFA→overview passes end-to-end — caught and fixed a real SPA
history-fallback bug (deep links / refresh 404'd). **Repo integrity:** fixed a `.gitignore` rule
(`secrets/`) that had gitignored the entire internal/dash/secrets envelope-encryption package since
P0, so the committed tree now builds from a clean checkout. Design-target stores
(Redis/ClickHouse/Kafka/Temporal) + WORM anchor recorded as deploy-time decisions. Residuals to
staging: full-scale multi-instance/10-min load, enrichd drain-gating (OI-P5-2), bulk auto-resume
(OI-KEYS-1c), recovery-code-on-step-up.

### 2026-07-06 — Waterfall Management Dashboard build (P0–P12) — control-plane + 12 module UIs + P12 hardening closure
Delivered the full admin dashboard for the enrichment engine across twelve one-commit phases on branch
`waterfall` (contract: `docs/waterfall-dashboard/12`). **Backend** (`internal/dash/*`, 21 packages, stdlib-only):
P0 identity/tenancy/session/audit spine (dual-GUC RLS `db`, `httpx` auth+CSRF+idempotency chain, `rbac`,
`security` pbkdf2+RFC-6238 TOTP, hash-chained `audit`, AES-256-GCM `secrets`) + `cmd/dashboardd` (env→pool→
migrations→routes→`/healthz` `/readyz` `/metrics`); P1 providers catalog + keys/pools + envelope-sealed 1k
CSV import; P2 rotation engine (12 strategies, batched quota leases, KM-3 trigger machine); P3 config
versioning + routing/waterfall validators + zero-egress dry-run; P4 telemetry backbone (usage_events + all
rollups) + provider health center + approvals quorum engine + leader-elected loops; P5 queues/workers read
model over `job_outbox` + pgoutbox redrive + heartbeat; P6 cost analytics + alerts evaluator/notifier
(SSRF-guarded); P7 overview 2s aggregator + multiplexed SSE realtime + Last-Event-ID replay. Migrations
0004–0010 (append-only, FORCE RLS on every table). **Frontend** (`web/`, Vite+React+TS, ADR-0016 locked deps):
P8 design system + typed api client + SSE manager + auth; P9 providers/keys(1k virtualized grid)/rotation/
health; P10 routing(dnd-kit)/workflows/queues/dead-letters/workers; P11 cost/alerts/security/approvals/settings
+ a11y. **P12 hardening (2026-07-06):** converted the runnable single-instance UNVERIFIED targets to measured
numbers in doc 13 §6 — L1 key-selection **24.7M sel/s** @ -cpu=8 (0 allocs, ~2,470× the 10k/s target;
`BenchmarkPoolSelect` + no-over-lease `TestRotationLeaseNoOverLease`), L2 SSE 200-client/20s soak **p99 12.27ms**
(≤2s), zero dropped changed events (`TestSSESoakLite`), L3 1k-key import sealed zero-plaintext, L4 100k-event
fold→refold **byte-identical** across 9 rollup tables; web bundle **111.2 KB gz** initial (budget 400 KB).
**Live boot smoke passed**: dashboardd booted against an ephemeral PG17 with bootstrap (10 migrations + `dash_app`
role provisioning), served the SPA + liveness/readiness/metrics, rejected the unauthenticated admin route (401),
completed a pbkdf2 login (operator→`mfa_required`, tenant_user→`ok`+csrf), and served six authenticated operator
reads (audit-verify `{ok:true}`, queues, dead-letters, overview, workers, audit-log) all 200; clean SIGTERM
shutdown. **Security pass:** secret scan clean (only synthetic test placeholders); RLS zero-rows release blocker +
fuzz + G2 replay + CSRF/idempotency/SSRF-notifier/formula-injection suites green via `scripts/run-rls-test.sh` on
PG17.10. **Chaos (covered subset):** aggregator-leader failover (`TestOverviewAggregatorFailover`,
`TestTelemetryLeaderElection`) + publish-crash consistency (`TestConcurrentPublishConflict`) satisfy their §7
invariants; PG-restart-reconnection + poison-import-row + publish-crash fault-injection deferred to staging.
**Runbook validation:** RB-5/6/7/12 Diagnosis/Verification read commands executed live against the booted
dashboardd (all 200). Gates: `go build ./... && go vet ./...` clean (47 packages); web `tsc --noEmit` + 192
vitest + no-orphan-UI + build green. Docs `waterfall-dashboard/00–14` flipped DRAFT→ACCEPTED; doc 00 §8 UNVERIFIED
register + doc 13 §6 load table updated with measured values; doc 12 §5 Self-Verification Record refreshed with
P12 measured evidence + closure line. **Honestly deferred (OI-P12-1..3):** full-scale/multi-instance load
(500-client/10-min SSE soak, 50k-row import, 1M-event fold, API p95 @ 200 rps), the remaining chaos drills +
RB-14 restore RPO/RTO, and the Playwright-against-live E2E run — all to a staging load-lab.

### 2026-07-01 — Implementation Slice 20 (Go) — config validation + startup self-check
Human approved making misconfiguration fail loudly at startup instead of per-request. New
`internal/config`: `Load(getenv)` (pure, unit-testable) validates PORT (1..65535), DSNs (must have
user=+dbname=), OUTBOX_MAX_ATTEMPTS (≥1), JWT_HS256_SECRET (≥16 bytes), and coherence (admin/relay
DSN require a primary DSN; POSTGRES_DSN and DURABLE_LOG are mutually exclusive), returning ALL
problems joined; `main` refactored to read the validated Config instead of scattered os.Getenv.
`cmd/enrichapi` `startupSelfCheck` (Postgres mode): refuses to start if the app connects as a role
that bypasses RLS (superuser/BYPASSRLS — would silently defeat G1) and if required tables are
absent. New primitives: `pg.Conn.RolePrivileges()` (super/bypassrls) and `pgmigrate.Pending()`
(migration drift). New `GET /readyz` (distinct from /healthz liveness) wired to `pgstore.Store.Ping`
— 200 only when the datastore is reachable. Live-verified (PG17): bad config logs all three errors
+ refuses to start; a superuser app DSN → refuses to start with the G1 message; memory-mode /readyz
→ ready; `TestRolePrivileges` + `TestPending_ReportsUnapplied` pass; the Slice-16 crash harness
still passes (40/40, happy-path self-check as app_rls). Unit: `config` (4) + `/readyz` (200/200/503).
OpenAPI declares /readyz. Mainline (99 tests) `go build/vet/test/gofmt` clean. New doc `docs/42`.
Continuous health, config-file loading, and relay/vendor readiness honestly deferred.

### 2026-07-01 — Implementation Slice 19 (Go) — consolidation: README, one-command demo, docs index
Human approved a consolidation pass to make the 18 slices approachable + runnable. Added a
top-level `README.md` (what it is, the five correctness gates G1–G5 + the "model proposes, gate
disposes" invariant, an architecture diagram, the stdlib-only property, a copy-pasteable
quickstart, the full unit/live/crash testing story, a repo map, and an explicit honest-deferrals
section — every claim backed by code or a test). Added `scripts/demo.sh`: one command, five phases
— build → unit suite → offline `enrichd` provenance demo → live HTTP round-trip against the gateway
in memory mode (real JSON + `/metrics`) → auto-detected live PostgreSQL harnesses (skipped
gracefully when PG17 is absent). Updated `docs/README.md` (replaced the stale "awaiting approval"
status with the real 18-slice state; indexed slices 23–40 + the top-level README). godoc audited
complete (no change needed). **Bugfix:** building the demo surfaced a real latent race in
`scripts/run-rls-test.sh` — five integration packages share one database but `go test` ran their
binaries in parallel, so `pgmigrate`'s drop/recreate intermittently raced `pgoutbox`'s setup;
fixed with `-p 1` (serialize). Re-verified: all 9 harness tests deterministic, and the
run-rls → crash-recovery chain tears down cleanly on the shared port. No Go source changed;
mainline (94 tests) unaffected. New doc `docs/41`.

### 2026-07-01 — Implementation Slice 18 (Go) — DLQ redrive / replay
Human approved closing the inspect-only-DLQ gap from Slice 17: an operator can now redrive a
parked job so the outbox re-delivers it after the bug is fixed. `Store.Redrive(ctx, jobID)` is one
RLS-scoped `UPDATE … WHERE job_id=$1 AND dead` that resets `dead=false, pending=true, attempts=0,
claimed_at=null, last_error=null, status='queued'` (payload untouched → same job re-runs, G2-safe)
and reports whether a dead row was reset. `POST /v1/dead-letters/{id}/redrive` is a write (gated on
the write scope, 403 without), tenant-scoped (G1), returns 404 when nothing dead matches, is
audit-logged (`dlq_redrive` with tenant+user+job) and counted (`dlq_redrive_total`); the
`DeadLetterLister` interface grew a `Redrive` method (now `DeadLetterAdmin`), wired via the same
decoupling adapter. Live-verified end-to-end (`TestPGOutbox_RedriveReplaysParkedJob`, PG17): park a
poison job → tenant-B redrive denied (RLS) → tenant-A redrive resets it and it leaves the DLQ → a
now-working worker re-delivers and completes it (`succeeded`, work_email filled) → a second redrive
of the completed job is a no-op. Writing the test caught the Slice-17 slow-job-vs-visibility hazard
(a 1ms visibility re-dead-lettered the in-flight job); fixed operationally (visibility > worker
time). OpenAPI declares the route (200/401/403/404). Mainline (94 tests) `go build/vet/test/gofmt`
clean. New doc `docs/40`. Bulk/auto/cross-tenant redrive honestly deferred.

### 2026-07-01 — Implementation Slice 17 (Go) — outbox dead-letter queue + max-attempts
Human approved closing the reliability gap flagged across Slices 13/16: the at-least-once outbox
redelivered a failing job forever. The gap is specifically the CRASH LOOP — a job that RUNS and
errors is already terminal (`failed`) and not redelivered; a job whose worker dies before any
terminal `Put` stays pending and loops. Migration `0003_outbox_dlq.sql` adds `attempts`/`dead`/
`last_error` + a partial dead index. `Relay.claim` (rewritten) increments `attempts` inside the
same atomic `UPDATE … FOR UPDATE SKIP LOCKED`; a claim that would exceed `maxAttempts` parks the
row (`dead=true, pending=false, last_error=…`) instead of delivering, and parked rows are never
scanned again. New `NewRelay` options `WithMaxAttempts(n)` (default 10) + `WithDeadLetterHook(fn)`;
tenant-scoped `Store.DeadLetters(ctx, limit)` + `GET /v1/dead-letters` (registered only when a
lister is wired). `cmd/enrichapi` wires `OUTBOX_MAX_ATTEMPTS`, the `outbox_dead_letter_total`
counter + a Warn log, and the DLQ endpoint via an adapter (keeps `api`/`pgoutbox` decoupled).
Live-verified (`TestPGOutbox_DeadLetterAfterMaxAttempts`, PG17): after 3 deliveries the next
claim parks the poison job, the hook fires exactly once, the tenant-scoped DLQ read returns it
(attempts>max, last_error set), further drains don't re-claim it, and tenant-B sees none (G1). The
Slice-16 crash-recovery harness still passes unchanged (2 pending at crash → 40/40 recovered, 40
ledger rows). Mainline (94 tests) `go build/vet/test/gofmt` clean. New doc `docs/39`. Redrive/replay,
slow-job-vs-visibility tuning, and alert routing honestly deferred.

### 2026-07-01 — Implementation Slice 16 (Go) — wire the full Postgres durable path into the binary
Human approved wiring everything built for Postgres over Slices 10–14 (RLS store, G2/G4 ledgers,
transactional outbox, migration runner) into `cmd/enrichapi` and proving it end-to-end through the
real binary. Datastore selection is now three-way, most-durable-first: `POSTGRES_DSN` → `pgstore`
engine/record store (RLS) + `pgoutbox` job store/submitter + a privileged `pgoutbox.Relay`
(FOR UPDATE SKIP LOCKED, 3s visibility) that recovers in-flight jobs after a crash; `DURABLE_LOG`
→ file-WAL; neither → in-process. When `POSTGRES_ADMIN_DSN` is set, startup runs the migration
runner and idempotently provisions two NON-superuser roles — `app_rls` (RLS-enforced) and `relay`
(BYPASSRLS, claim only) — so a fresh cluster comes up ready yet tenant isolation (G1) is enforced
at runtime (the app is not a superuser/owner and cannot bypass RLS). New
`scripts/crash-recovery-test.sh` drives the real compiled binary against an ephemeral PG17
cluster: submit 40 async jobs → `kill -9` (a crash) → restart → assert all complete. Observed:
40 durably captured, **3 still pending at the kill**, **40/40 records recovered**, 40 outbox rows
delivered, **40 idempotency-ledger rows for 40 jobs (G2: no double execution on redelivery)**,
0 pending — **PASS**. Mainline (94 tests) `go build/vet/test/gofmt` clean. New doc `docs/38`.
Trust/superuser bootstrap, shell-harness (not go test), single-relay, and DLQ honestly deferred.

### 2026-07-01 — Implementation Slice 15 (Go) — real-provider HTTP smoke + pinned fixtures
Human approved exercising the real vendor adapters (Hunter/Prospeo/Twilio) end-to-end through the
egress key-injection seam against mock vendor servers, and pinning the assumed response shapes as
checked-in fixtures to narrow the no-fabrication gap on vendor wire formats. Added
`testdata/{hunter_found,hunter_empty,prospeo_found,twilio_found}.json` + `README_UNVERIFIED.md`
(states the `UNVERIFIED` marker + the exact promotion path: sandbox key → capture raw 2xx →
reconcile Decode → record source_url/verified_date). New `live_smoke_test.go`:
`TestAdapters_DecodeRecordedFixtures` (each adapter decodes its pinned fixture through the real
`AuthInjector`; empty Hunter data → no observation, not an error), `TestAdapter_EgressSSRFBlocked`
(a real adapter through `NewEgressClient` to an http/loopback host is refused before connecting —
`ErrSSRFBlocked` → non-retryable BAD_REQUEST — the SSRF choke is live on the adapter path), and
`TestAdapters_StatusErrorMatrix` (401→AUTH, 402→QUOTA, 403→RATE_LIMIT, **404→NOT_FOUND**,
429→RATE_LIMIT, 400→BAD_REQUEST, 500→TRANSIENT, 503→PROVIDER_DOWN). VERIFIED: auth scheme +
injection and status→error-class mapping. Still UNVERIFIED (honestly): the JSON field names —
now a single tested, labelled place. Mainline (94 tests) `go build/vet/test/gofmt` clean. New doc
`docs/37`. No live vendor was called (requires an authorized key + approval).

### 2026-07-01 — Implementation Slice 14 (Go) — SCRAM-SHA-256 auth + TLS + migration runner
Human approved hardening the stdlib `pg` client for real deployments (still zero external deps).
**SCRAM-SHA-256** (RFC 5802/7677, no channel binding) implemented in `pg/scram.go` — PBKDF2 via
Go 1.24 `crypto/pbkdf2`, client-proof = ClientKey XOR HMAC(StoredKey, AuthMessage), and mutual
auth (the server-final verifier is checked constant-time); wired into the startup handshake as
SASL (auth code 10). **TLS**: the `SSLRequest` negotiation + `crypto/tls` upgrade, exposed via
`Config.TLS` and DSN `sslmode` (libpq semantics: require / verify-ca / verify-full). **Migration
runner** (`internal/pgmigrate`): applies `NNNN_*.sql` in order into a `schema_migrations` table,
each file + its version row in one transaction (atomic, idempotent); migrations 0001/0002 had
their `BEGIN/COMMIT` removed so the runner owns the transaction. Verified: `TestSCRAM_RFC7677Vector`
(mainline — exact client proof + server verifier against the RFC worked example),
`TestConn_SCRAM` (live — real scram password role), `TestConn_TLS` (live — `pg_stat_ssl` confirms
the backend is encrypted), `TestApply_OrderedAndIdempotent` (live — ordered, no-op re-apply).
**9 live integration tests** now pass on PostgreSQL 17.10; mainline (91 tests) clean. New doc
`docs/36`. Channel binding (SCRAM-PLUS), MD5, cert-verify-by-default, and down-migrations honestly
deferred.

### 2026-07-01 — Implementation Slice 13 (Go) — Postgres transactional-outbox durable queue
Human approved replacing the file-WAL durable queue (Slice 03) with a Postgres transactional
outbox. New migration `0002_job_outbox.sql`: one `job_outbox` row per job (payload jsonb +
`pending` intent + `claimed_at`), RLS + FORCE, partial index over pending rows. `pgoutbox.Store`
(drop-in `job.Store` + `job.Submitter`) mirrors the file-WAL semantics: `Submit` durably captures
the job (`INSERT ... ON CONFLICT DO NOTHING`, never sheds), `Put` clears `pending` iff terminal in
the same UPDATE as the snapshot, `Get` is RLS-scoped — all tenant-GUC-bound (G1;
`ErrTenantMismatch` on a cross-tenant submit). `pgoutbox.Relay` claims pending rows with `FOR
UPDATE SKIP LOCKED` (competing consumers — multiple replicas poll without double-claiming) and a
visibility timeout that recovers a crashed relay's in-flight claims. Live-verified on PostgreSQL
17.10 (`TestPGOutbox_DurableDeliveryAndCrashSafety`): normal delivery (provider once; outcome
round-trips through JSONB; completed job not re-claimed); **crash + redelivery** (reset row to
pending → re-claimed → re-run → **0 new provider calls**, G2 exactly-once-effective);
visibility-timeout (recent claim skipped, stale claim recovered); tenant isolation on reads.
Mainline `go build/vet/test/gofmt` clean; **6 live integration tests** now pass. New doc `docs/35`.
Not wired into cmd (needs an app role + a BYPASSRLS relay role provisioned by ops); same-txn event
outbox and DLQ/max-attempts honestly deferred.

### 2026-07-01 — Implementation Slice 12 (Go) — Postgres G2/G4 ledgers + connection pool
Human approved porting the last two ledgers to Postgres so EVERY correctness gate is enforced
at the datastore with RLS, not just G5. `pgstore` is now a full `store.Store`. **G2**
(idempotency): `Record` uses `INSERT ... ON CONFLICT DO NOTHING` (first-writer-wins;
`provider.Result` stored as jsonb), `Lookup` JSON-decodes it back — RLS-scoped so a tenant can
neither read nor forge another's entry. **G4** (cost): the reservation is a single guarded
`UPDATE ... WHERE committed + amount <= ceiling RETURNING committed` — zero rows ⇒
`ErrCeilingExceeded` with no change; a row lock serializes concurrent reservations so the
ceiling holds under contention; `Release` refunds via `GREATEST(0, ...)`. Added `internal/pg.Pool`,
a bounded connection pool (token-capped open conns; reuse; broken-eviction) so each op checks
out a connection, runs one transaction that binds the tenant GUC `SET LOCAL`, and returns it —
never sharing a connection across tenants mid-transaction. The full-stack E2E now uses `pgstore`
as the ENTIRE store (G2/G4/G5 all datastore-durable) and additionally asserts the ledger tables
are non-empty post-run. New tests: `TestPool_BoundsAndReuse` (mainline, injectable dialer),
`TestPG_IdempotencyLedger` + `TestPG_CostLedger` (live: round-trip, first-writer-wins,
ceiling-rejection-leaves-state, tenant isolation on both ledgers). **5 live integration tests
pass on PostgreSQL 17.10**; mainline (89 tests) `go build/vet/test/gofmt` clean. New doc
`docs/34`. **⭐ All five gates now datastore-enforced with RLS + live-verified.** SCRAM/TLS,
migration runner, pool liveness checks, and a Postgres transactional outbox honestly deferred.

### 2026-07-01 — Implementation Slice 11 (Go) — full-stack end-to-end test (live)
Human approved a black-box, full-stack integration test proving the wired system upholds the
gates end-to-end. `internal/e2e` drives a real signed **JWT → HTTP gateway → async queue +
worker pool → Execution Engine → live PostgreSQL (FORCE RLS) → HMAC-signed webhook**; only the
vendor providers are deterministic fakes (they count calls for the G2 assertion), everything
between the JWT and the database is production code. Asserted over HTTP against a live cluster:
**G1** — a second tenant's `GET /v1/records` returns 0 fields (isolation enforced by the
database, not app code); **G2** — a second job for the same record+field+params triggers 0 new
provider calls (served from the idempotency ledger); **G4** — a `cost_ceiling:2` job against a
6-credit provider commits ≤ 2 (no overspend); **G5** — the value read back from Postgres carries
full provenance; plus a signature-valid, tenant-bound webhook delivered on completion. All pass
live in ~0.18s. Composite store binds G5→Postgres, G2/G4→memory (PG port later). The webhook
egress guard is bypassed in this test only (loopback sink; SSRF is unit-tested in Slice 05).
Added to `scripts/run-rls-test.sh`; mainline `go build/vet/test/gofmt` clean. New doc `docs/33`.

### 2026-07-01 — Implementation Slice 10 (Go) — Postgres store + live tenant-isolation (RLS) proof
Human approved closing the biggest prototype→production gap: gate G1 enforced by the DATABASE
via row-level security, proven live. To preserve the zero-external-dependency property, added
`internal/pg` — a stdlib PostgreSQL wire-protocol (v3) client: startup (trust/cleartext),
simple + extended (Parse/Bind/Execute/Sync) query protocols with **bound parameters** (no SQL
injection), text decoding with NULLs, structured `PGError`, and post-error `Sync` recovery.
Added `internal/pgstore` — a `store.FieldVersions` (G5) implementation whose every op runs in a
transaction binding `SET LOCAL app.current_tenant` from the request **principal** (never an
argument), with `Append` stamping `tenant_id = current_setting(...)` so the RLS `WITH CHECK`
confines writes to the caller's partition; fails closed with no principal. The migration
(`0001_init.sql`, `FORCE RLS` + `USING`/`WITH CHECK`) was applied against a **real PostgreSQL
17.10** and the docs/21 §1 release-blocker test **passed live**: run as a NON-superuser role
(superusers bypass RLS), cross-tenant read returns **0 rows**, `WITH CHECK` rejects a
cross-tenant INSERT, the app store isolates by principal, and an unauthenticated context is
rejected. Reproducible via `scripts/run-rls-test.sh` (ephemeral trust cluster or
`WATERFALL_PG_DSN`). Integration tests are `-tags integration` + DSN-gated; mainline
`go build/vet/test/gofmt` stays clean. New doc `docs/32`. **⭐ G1 datastore release-blocker
satisfied + live-verified.** G2/G4 Postgres ledgers, connection pooling, in-client SCRAM/TLS,
and a migration runner honestly deferred.

### 2026-07-01 — Implementation Slice 09 (Go) — real JWT auth (verified signed tokens)
Human approved replacing the static dev-token stand-in with real JWT verification (RFC
7519/7515), so the tenant principal driving G1 is now a cryptographically verified claim.
`internal/auth`: stdlib-only verifier (HS256 + RS256) with **`kid` rotation** and the
hardening a JWT verifier lives by — **the alg is pinned by the key, not the token header**
(defeating `alg:none` and the RS256→HS256 confusion attack), constant-time HMAC compare, `exp`
required + `nbf`/`iss`/`aud` validated with bounded clock leeway, and **`tenant_id` required &
non-empty** so G1 can never fall back to an ambient tenant. Signing lives only in a test-support
package (`authtest`); the production package verifies, never signs. `api.JWTAuthenticator` slots
into the existing `Authenticator` seam (gateway otherwise unchanged); a new optional
`Server.WriteScope` gives **scope-based authz** — a verified-but-unauthorized token is **403**,
distinct from 401, and any verification failure is 401 with no leak of which check failed.
`tenant.Principal` gained `Scopes`/`HasScope`. `cmd/enrichapi` enables JWT when
`JWT_HS256_SECRET` is set (else warns + falls back to dev tokens). 6 new tests (88 total): valid
HS256/RS256+rotation, a rejection table (expired, nbf, wrong iss/aud, missing tenant, unknown
kid, tampered payload, alg:none, malformed, wrong secret, **alg-confusion**), array-audience,
leeway; plus end-to-end API auth+scope. `go build/vet/test/gofmt` clean. **Live-verified:**
JWT-enabled service with externally-minted HS256 tokens → 202/403/401 across the matrix. New doc
`docs/31`. JWKS discovery, RS256 PEM/mTLS, and token revocation honestly deferred.

### 2026-07-01 — Implementation Slice 08 (Go) — calibration + bandit routing (learned components)
Human approved adding the two *learned* pieces of the methodology under the invariant "model
proposes, deterministic gate disposes". `internal/calibrate`: isotonic regression via PAVA — a
monotonic, opt-in, offline-fitted `raw score → P(correct)` map per `(provider, field)`, applied
**before** fusion (the fuse/SPRT now operate on calibrated confidence) while **provenance keeps
the raw provider score** (G5 intact). `internal/bandit`: dependency-free Beta-posterior Thompson
sampler (Marsaglia-Tsang Gamma→Beta) with a **conservative floor** (blend toward the static prior
until enough pulls) and a **seed-reproducible** per-plan scorer. New `router.Scorer` seam
(`WithScorer`) orders the cascade by sampled score/cost; bandit satisfies it structurally (no
import cycle); default preserves exact static-prior behavior. Engine `WithCalibrator`/`WithBandit`
close the loop — the engine updates the bandit after *real* calls only (cache hits don't
double-count) and the gates (G1–G5) are untouched. Wired into `cmd/enrichapi` with a per-request
seeded scorer (race-free). 10 new tests (82 total): PAVA monotonicity + overconfidence
correction, opt-in/nil-identity, posterior shift, no-data⇒prior, replayable scoring, sample-range;
router reorder; **closed-loop learning over 40 records** (mean(good) > 0.6 > 0.5 > mean(bad)) and
calibration-reflected-in-resolved-value. `go build/vet/test/gofmt` clean. New doc `docs/30`.
Online calibration/label-feedback, contextual/cost-aware regret bounds, and durable/shared bandit
state honestly deferred.

### 2026-07-01 — Implementation Slice 07 (Go) — observability (metrics + structured logs)
Human approved the observability increment. Added `internal/metrics` — a dependency-free,
concurrency-safe Prometheus registry (labeled counters/gauges/gaugefuncs/histograms → text
exposition). Instrumented the API with **RED golden signals** (`http_requests_total`,
`http_request_duration_seconds`) + a `/metrics` endpoint + one structured `slog` line per request
using the **route template** (never the concrete path → no PII in labels/logs). Instrumented the
engine with provider health + **enrichment KPIs** (`provider_calls_total{provider,result}` incl.
`breaker_open`/`blocked`, `provider_call_duration_seconds`, `provider_cost_credits_total`,
`enrichment_fields_filled_total`). Added `queue_depth` GaugeFunc + `webhook_deliveries_total`.
7 new tests (72 total): registry rendering/escaping/re-register, `/metrics` RED with `{id}`
template + **no leaked id**, engine cost/fields metrics. `go build/vet/test/gofmt` clean.
**Live-verified:** scraped `/metrics` after a job — per-vendor calls, cost summing to 13 (the
waterfall spend), fields filled, queue depth, HTTP RED. New doc `docs/29`. Tracing + dashboards
+ per-tenant metrics (cardinality) honestly deferred.

### 2026-07-01 — Implementation Slice 06 (Go) — webhooks-out (tenant-bound) + OpenAPI
Human approved the webhooks + OpenAPI increment. Added a Dispatcher `OnComplete` hook (fires
after the durable-terminal state, decoupling `job` from `webhook`) and `internal/webhook`: HMAC-
SHA256 signed completion callbacks delivered **tenant-bound** (URL only from the delivering
tenant's registered config, resolved by tenant_id — no cross-tenant PII egress, G1) and
**SSRF-safe** (through a per-tenant egress allow-list, wiring the Slice-05 seam), with bounded
retries (5xx/429 retried, other 4xx terminal) and skip-when-unconfigured. Added `docs/api/
openapi.json` (OpenAPI 3.0.3) + a dependency-free **contract test** binding spec↔impl (every
status code the API returns for a representative request must be declared). Wired the webhook
sender into `cmd/enrichapi` via the hook (env-configured, inert by default). 8 new tests (65
total): sign/verify, signed POST, **tenant-binding (0 cross-tenant hits)**, unconfigured no-op,
bounded 5xx retries, 4xx terminal, OpenAPI contract match. `go build/vet/test/gofmt` clean. New
doc `docs/28`. (No live loopback smoke: the egress guard correctly blocks 127.0.0.1 — by design.)

### 2026-07-01 — Implementation Slice 05 (Go) — egress-proxy / SSRF choke
Human approved the SSRF-choke increment (the #2 security risk). Added `internal/provider/ssrf.go`:
a hardened egress client layering **HTTPS-only + FQDN allow-list** (`hostGuard`) → **key
injection** (Slice 04) → **dial-time IP guard** (`NewEgressTransport` dialer `Control` validates
the resolved IP, refusing metadata/RFC1918/loopback/ULA/link-local/CGNAT/0.0.0.0-8/IPv4-mapped —
DNS-rebinding- and encoding-safe), with redirects re-checked + capped. `ErrSSRFBlocked` classified
non-retryable BAD_REQUEST in adapters. 4 new tests (57 total): the SSRF **corpus** (17 internal
addresses blocked, publics pass, nil fails closed), real loopback dial blocked at the guard,
hostGuard https/allow-list enforcement, full-client metadata refusal. `go build/vet/test/gofmt`
clean. New doc `docs/27`. **Both top-2 risks now enforced in code + tested (G1 + P2 SSRF).**
Documented that a network-level default-deny egress is still required (belt-and-suspenders).

### 2026-07-01 — Implementation Slice 04 (Go) — real provider adapters + egress key-injection seam
Human approved the real-adapters increment. Added `internal/provider/egress.go` (KeyResolver +
AuthInjector RoundTripper injecting the credential by header/query/bearer/basic AS the request
leaves — adapters stay **secret-free**) and `internal/provider/adapters/` with three concrete
API-first vendors: **Hunter** (query api_key; 403→RATE_LIMIT), **Prospeo** (X-KEY header;
402→QUOTA), **Twilio Lookup** (HTTP Basic; 404→NOT_FOUND). Extended the canonical Field vocab
with `first_name`/`last_name`/`full_name` (email-finder match keys; `docs/00` §7 — back-prop).
6 new tests (53 total): per-vendor contract + injection-seam + error-taxonomy, plus
`TestAdapters_EngineIntegration` (two real adapters through Router+Engine fill work_email +
phone_status with G5 provenance). Vendor wire formats honestly marked `UNVERIFIED`/representative
(confirm vs live API before prod; risk localized to Build/Decode). `go build/vet/test/gofmt` clean.
New doc `docs/26`. The egress-proxy slice (SSRF choke) is the natural follow-on — it wraps this seam.

### 2026-07-01 — Implementation Slice 03 (Go) — durable queue + transactional outbox
Human approved the crash-safety increment. Added `internal/durable`: a `fsync`'d framed
write-ahead **Log** (CRC + atomic commit-marked batches + **torn-tail recovery**), a durable
**Store** implementing the **transactional outbox** (job snapshot + publish-intent appended
atomically; intent cleared only on durable-terminal, making execution crash-safe), and a
**Relay** (outbox→queue, at-least-once re-drive on recovery). Refactored the API onto a
`job.Submitter` seam (in-process `QueueSubmitter` OR durable store); `cmd/enrichapi` selects
durable via `DURABLE_LOG`. **At-least-once redelivery is charge-safe via engine G2** (proven
by `TestPipeline_CrashRecoveryExactlyOnceCharge`). 5 new tests (47 total); `go build/vet/test/
gofmt` clean. **Live-verified:** async job survived a full process kill+restart — `GET` after
restart returned the recovered succeeded outcome from the on-disk WAL. New doc `docs/25`;
deferred scope (distributed Kafka/Redpanda log + DB outbox/CDC, field-data durability, log
compaction, group-commit) logged, not hidden.

### 2026-07-01 — Implementation Slice 02 (Go) — API gateway + async job queue
Human approved the API + queue increment. Added `internal/api` (REST gateway: auth→principal
G1, Idempotency-Key writes, per-tenant rate limit, taxonomy→HTTP, validation) + `internal/job`
(tenant-scoped JobStore, bounded two-lane priority Queue with back-pressure shedding, worker-pool
Dispatcher running the engine under the submitter's principal, panic-contained) + `cmd/enrichapi`
(gateway + 8 workers, graceful shutdown). **All five gates preserved across the new surface**;
API-level idempotency added on top of provider-call G2. 20 new tests (42 total); `go build/vet/
test/gofmt` clean; **live HTTP smoke passed** (sync enrich 0.911 email + 13/15 credits w/
provenance; 400 no-key; 401 no-auth; 409 key-reuse; **404 cross-tenant job read**; 429 rate limit).
New doc `docs/24`; deferred scope (durable queue+outbox, real JWT, egress-proxy, webhooks, OpenAPI)
logged, not hidden.

### 2026-07-01 — Implementation Slice 01 (Go) — correctness-gate vertical slice
Human approved implementation (thin vertical slice, Go). Installed Go 1.26.4 locally.
Built an end-to-end enrichment path in `internal/` (`domain`, `tenant`, `provider`,
`router`, `engine`, `store`) + `cmd/enrichd` demo + `migrations/0001_init.sql` (FORCE RLS).
**All five gates enforced in code and each proven by a test** (G1 cross-tenant negative
test = release-blocker; G2 replay = no double call/charge; G3 timeout/retry-bound/breaker;
G4 reserve-before-call never exceeds ceiling + charge-on-success refund; G5 store rejects
bare values). `go build/vet/test/gofmt` clean; coverage 68–89% on covered pkgs. Demo shows a
live waterfall (cheap→premium email fused to 0.911, phone 0.88, 13/15 credits, idempotent
replay = 0 new calls). Documented in `docs/23`; deferred scope (Postgres integration test,
egress-proxy, queue, API, real adapters, calibration) logged, not hidden. New doc `docs/23`.

### 2026-07-01 — Planning Completion Gate — adversarial review + fixes
5-reviewer adversarial audit (`wf_15689f67-653`) of the whole repo. **5 blocking FAILs found and fixed:**
(B1) adapter-holds-secret contradiction → auth-descriptor + egress key injection; (B2) idempotency-key
canonicalized across skill/`04`/`09`/`10`/`erd`; (B3) ClickHouse tenant isolation compensating control
(row policy + mandatory predicate + CI test); (B4) outbound webhook allow-list made tenant-bound (closes
cross-tenant PII egress); (B5) ADR index + footer corrected (0015). WARNs addressed: intent-lane G3+egress,
outbox boundary enumeration + CDC relay, SSRF IP-encoding-bypass, audit immutability (hash-chain+WORM),
Little's-Law harmonized (350 ms), glossary "account" note, SSOT diagram map, tracker de-dup. Accepted gaps
(GraphQL/gRPC deferrals, artifact-level items, QS-TMP-1, secrets-backend, UNVERIFIED numbers) logged in
`IMPLEMENTATION_PROGRESS.md` §PCG. **Gate = PASS; awaiting human approval to implement.**

### 2026-07-01 — Phases 17–22 (ops & product) — auto-advance batch
- `17-Dashboard-Planning.md` — every panel mapped to a backing service/table; RBAC/ABAC scope.
- `18-Security.md` — consolidated model: two-layer tenant isolation (P1), SSRF (P2, ref `13`), RBAC/ABAC,
  Vault/KMS, residency + compliance map (incl. data-broker/DNC/consent), STRIDE, DR (RPO≤5m/RTO≤1h).
- `19-Deployment.md` + `deployment.mmd` + `infrastructure.mmd` + **ADR-0015** (portability-first, AWS
  reference, regional cells, blue-green/canary, default-deny egress zones).
- `20-Monitoring.md` — golden signals + enrichment KPIs (hit-rate/lift/cost-per-match) + SLOs + security telemetry.
- `21-Testing.md` — negative gate tests (G1–G5, release blockers) + load test (turns throughput
  UNVERIFIED→VERIFIED) + SSRF corpus + chaos + DR drills; every `UNVERIFIED` assumption mapped to a test.
- `22-Future-Roadmap.md` — deferred backlog (incl. QS-TMP-1 Temporal spike).
- **All 22 planning docs now IN-REVIEW; 9 diagrams complete; ADRs 0000–0015.** → Planning Completion Gate.

### 2026-07-01 — Phase 10 (Queue System) — auto-advance
- `10-Queue-System.md` + `queue-flow.mmd` + `retry-flow.mmd` from a 7-technology cited tradeoff
  workflow (`wf_2013b0cd-df8`). **Two orthogonal decisions:** **ADR-0013** async transport = Kafka-
  protocol log (Redpanda preferred) — chosen for lag back-pressure + replay + multi-cloud portability
  (SQS/Pub/Sub rejected as single-cloud; RabbitMQ wrong back-pressure model); **ADR-0014** orchestration
  = Temporal durable execution (deletes hand-rolled Saga/outbox/checkpoint + native tenant fairness),
  **cost-gated** on an Action-volume spike (**QS-TMP-1**, flagged to human) with documented fallback =
  hand-rolled Saga+outbox on the same backbone. Redis KV = idempotency store.
- Back-propagated: `05` workers-as-Temporal-workers; `09` §5 checkpoint via Temporal (both conditional).

### 2026-07-01 — Phases 5–9, 11–16 (core architecture) — auto-advance batch
Per human-approved cadence (auto-advance 5–16, stop only for FAILs/decisions), authored from the
established ADRs; each doc carries its own recorded gate checklist. Phase 10 (Queue) pending its
tradeoff-research workflow.
**Added / rewritten**
- `05-Microservices.md` (+ `dependencies.mmd`) — module/service catalog + boundary rules.
- `06-Database-Architecture.md` (+ `erd.mmd`) + **ADR-0011** (Postgres RLS-pool + Redis + ClickHouse).
- `07-API-Gateway.md` + **ADR-0012** (REST + webhooks external, gRPC internal, GraphQL deferred).
- `08-Waterfall-Orchestrator.md` — full routing/plan spec (answers every ordering question).
- `09-Execution-Engine.md` — deterministic gate spine (G2/G3/G4 re-checked per call; G5 structural).
- `11-Scaling-Strategy.md` — Little's-Law sizing, per-provider budgets, finite autoscaling.
- `12-Provider-Key-Management.md` — key pools, health, continuity, correlation graph.
- `13-Proxy-Management.md` — SSRF-safe egress choke (top-2 risk), key injection at proxy.
- `14-Intent-Engine.md`, `15-Verification-Engine.md` — providers cited from `03`.
- `16-Cost-Optimization.md` — ceilings, charge-on-success, cache-before-reveal.

### 2026-07-01 — Phase 4 (System Architecture) complete → at GATE
**Added**
- `docs/04-System-Architecture.md` — end-to-end system design via a 3-proposal/3-judge design panel
  (`wf_2099540b-a5f`). Winner: **hybrid modulith control-plane + elastic stateless data-plane** (best
  cost/p95 balance meeting scale + isolation), with microservices-proposal hardening grafted in.
- **ADR-0010** — architecture style + topology + sync/async boundary + two-layer tenant identity +
  keys-injected-at-egress + config-as-versioned-data + regional cells.
- Diagrams: **replaced** `architecture.mmd` (real component graph), **added** `api-flow.mmd` +
  `event-flow.mmd`.

**Structural gate enforcement documented:** G1 (FORCE RLS + signed principal context), G2 (Postgres
ledger + Redis fast-path + seeded RNG), G3 (Redis-shared breakers), G4 (atomic pre-flight reservation),
G5 (merge-then-write with NOT NULL provenance FK), SSRF (default-deny egress; only proxy has internet).

**Back-propagated:** `05` MS-2 decided (modulith); `06` DB-1 provisional (Postgres RLS-pool + ClickHouse)
to ratify in Phase 6; `10` QS-1 placement decided, engine to ratify in Phase 10.

**Open at gate:** engine choices (datastore SA-3, queue SA-4) explicitly deferred to their phase ADRs.

### 2026-07-01 — Phase 3 (Provider Research & Matrix) complete → at GATE
**Added**
- `docs/03-Provider-Research.md` — 28 providers researched + adversarially citation-verified via
  workflow `wf_f5d38fad-6f3` (56 subagents, ~1.84M tokens, 798 fetches; 672 claims, 38 downgraded).
  Combined with 18 Phase-1 providers → **46-provider roster** across all 22 required categories.
  Includes the **capability→provider coverage map + per-field seed waterfall ordering** (feeds ADR-0007).
- **ADR-0009** — provider inclusion/exclusion criteria: resolves the "scraped-provenance ⇒ exclude"
  inconsistency (Apollo/ZoomInfo also ingest public-web data yet are ACTIVE). 2 hard EXCLUDED
  (Proxycurl — LinkedIn litigation/wind-down; Datanyze — defunct/absorbed); 3 DEPRIORITIZED
  (Kaspr, ContactOut, Coresignal) pending a human policy decision (**PR-EXCL-1**).

**Back-propagated (audit loop)**
- `08` OR-4 cold-start ordering now seeded from `03` §3; `12` provider correlation/ownership graph
  (copy-discount for ADR-0005); `14` intent/signal providers confirmed; `15` verification providers
  confirmed; `18` provenance/compliance gating for DEPRIORITIZED providers.

**Open at gate:** **PR-EXCL-1 needs a human policy decision**; all latency `UNVERIFIED` (load test);
identity/domain-intel provider specifics provisional (heavy downgrades).

### 2026-07-01 — Phase 2 (Waterfall Methodology) complete → at GATE
**Added**
- `docs/02-Waterfall-Research.md` — 5 methodology tracks (identity resolution, confidence aggregation,
  truth discovery/merge, cost-aware ordering, learned routing) researched + adversarially
  citation-verified via workflow `wf_8ebd6dba-440` (10 subagents, ~421K tokens, 199 fetches; 46
  methods, 2 downgraded, **0 hallucinated references**). Includes the adopted end-to-end pipeline.
- `diagrams/enrichment-pipeline.mmd` — canonical per-record methodology pipeline.
- **ADR-0004** (tiered identity resolution), **ADR-0005** (calibrate-then-fuse confidence + SPRT),
  **ADR-0006** (deterministic online merge + PROV), **ADR-0007** (Pandora reservation-value ordering),
  **ADR-0008** (Thompson routing inside deterministic G3/G4 gate).

**Governing invariant adopted:** "model proposes, deterministic gate disposes" — learned components
rank/propose; the Execution Engine re-enforces G3/G4 before every call; merge is rule-deterministic.

**Back-propagated (audit loop)**
- `08` ordering=Pandora + routing=Thompson + SPRT stop (OR-2/OR-3 now decided).
- `09` calibrate→fuse→SPRT + deterministic merge + tiered identity references.
- `06` new model additions (identity_clusters, calibrators, reliability weights, reservation values,
  bandit posteriors, W3C PROV field lineage).

**Open at gate:** WQ-1…WQ-11 (`ACCEPTED`) parameterize the chosen methods; resolved with measured
provider data (`03`) or the implementation feedback loop.

### 2026-06-30 — Phase 1 (Market Research) complete → at GATE
**Added**
- `docs/01-Market-Research.md` — 18 competitors researched + adversarially citation-verified via
  workflow `wf_6a361ade-28c` (36 subagents, ~1.08M tokens, 464 web fetches). Includes a comparison
  matrix, per-competitor cited entries with verification markers, executive synthesis, and an
  architecture-takeaways mapping. 27 of 144 sampled citations were downgraded to `UNVERIFIED`.

**Findings → decisions**
- Only Clay + BetterContact are true waterfall orchestrators; all other surveyed vendors are leaf
  sources with region/segment gaps → validates building an orchestrator with regional ordering.
- Clearbit standalone Enrichment API `DEPRIORITIZED` (sunset 2026, HubSpot-only).

**Back-propagated (audit loop)**
- `api-integration` skill: added 402=credit-exhaustion→failover + Hunter 403=throttle quirk + ingest
  quota headers.
- `08` per-(provider,field,region) confidence ordering + search/preview→reveal.
- `09` defensive field typing + provider-aware chunking + HMAC webhook fan-in.
- `12` provider supply-continuity health signal; `16` charge-on-success + Data-Credits/compute split
  + cache-before-reveal; `18` compliance map += data-broker registration/DNC/consent; `20` waterfall
  KPIs (hit-rate, incremental lift, cost-per-match) + continuity monitoring.

**Open at gate**
- 27 downgraded claims now `UNVERIFIED` (honest gaps, `ACCEPTED-RISK`); `✓` (un-re-checked) claims
  to be deepened in Phase 3 for chosen providers.

### 2026-06-30 — Phase 0 (Bootstrap) complete
**Added**
- Repository scaffolding: `/docs`, `/adr`, `/skills`, `/agents`, `/commands`, `/diagrams`; git init; `.gitignore`.
- `docs/README.md` — documentation root, status + verification legends, gate sequence.
- `docs/00-Project-Overview.md` — scope, **canonical Glossary (§7)**, throughput target as a tested
  assumption with supporting math, success criteria, highest-risk areas (tenant isolation + SSRF).
- `docs/00b-Tooling-And-Agents.md` — index + contract for all Phase 0 tooling.
- Skills: `enrichment-discipline`, `provider-research`, `waterfall-correctness`, `api-integration`,
  `doc-consistency`.
- Agents: Research, Architecture Reviewer, Gap-Analysis, Security Auditor, Implementation,
  Cost/Scale Reviewer.
- Commands: `/provider-audit`, `/architecture-review`, `/security-audit`, `/scale-check`,
  `/gap-analysis`, `/gate-check`.
- ADRs: 0000 (template), 0001 (record decisions), 0002 (API-first, no scraping), 0003 (plan-first
  gated process). ADR index in `adr/README.md`.
- Trackers: `docs/IMPLEMENTATION_PROGRESS.md`, this changelog.
- Doc stubs `01`–`22` with consistent headers, status, and Open-items tables.
- `diagrams/architecture.mmd` placeholder (to be replaced in Phase 4).

**Decisions**
- API-first only; no scraping/automation/manual workflows (ADR-0002).
- Plan-first, gate-driven process with human approval at gates (ADR-0003).

**Notes / deferred**
- All per-provider/competitor facts remain `UNVERIFIED` until cited in Phases 1/3.
- Throughput target (2,000 rec/s) is an engineering **assumption** pending load test (Phase 21).
- Optional `.claude/` mirror of skills/commands deferred as an enhancement.
