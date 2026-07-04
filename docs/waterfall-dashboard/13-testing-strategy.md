# 13 — Testing Strategy

> **Status:** DRAFT · **Owner:** Senior Backend Engineer · **Last updated:** 2026-07-02 · **Gated by:** /architecture-review, /security-audit

> Extends the repo gate-test discipline (`docs/21-Testing.md`): mandatory negative tests for
> **G1 tenant isolation, G2 idempotency, G3 bounded execution, G4 cost ceiling, G5 provenance**
> are release blockers, and this doc is where the dashboard's UNVERIFIED design targets become
> measured (§6). Governing invariant tested throughout: "the model proposes, a deterministic gate
> disposes." All API assertions use the doc 04 conventions: `/v1/admin/*`, snake_case JSON,
> `Idempotency-Key` on writes, uniform error body `{"error":{"code","message"}}`, cursor
> pagination with limit cap 200.

## 1. Test taxonomy and where each layer lives

| Layer | Tooling | Location | Trigger / gating | Datastore |
|---|---|---|---|---|
| Go unit | `go test` (offline: no network, no PG, injectable clocks) | `internal/dash/<feature>/*_test.go` + `<pkg>test` helpers | every build; CI mainline | in-memory fakes |
| Go integration | `go test -tags integration`, gated on `WATERFALL_PG_DSN`; run serially (`-p 1`) via the extended `scripts/run-rls-test.sh` | `internal/dash/<feature>/*_integration_test.go` | CI job with live PostgreSQL 17; **release blocker** rows marked below | live PG, non-superuser `app_rls` |
| Contract | `go test` over `docs/waterfall-dashboard/openapi-admin.yaml` | `internal/dash/httpx/openapi_admin_test.go` (mirrors `api/openapi_test.go`) | every build; **release blocker** | none |
| Frontend unit | `vitest` (jsdom; mocked `fetch`/`EventSource`, no network) | co-located `web/src/**/*.test.ts(x)` — each `features/<feature>/`, `lib/`, `api/` file's tests sit beside it (doc 08 §2): cursor helpers, `lib/status.ts` totality, SSE event routing, permission mirror, token-contrast (doc 08 §6.1) | every build; CI mainline | none |
| E2E | Playwright | `web/tests/e2e/` | CI against a seeded dashboardd + live PG | live PG |
| Load | `go test -bench` + `scripts/load/*.go` | `scripts/load/` | P12, then on demand; converts UNVERIFIED→measured (§6) | live PG |
| Chaos | `scripts/chaos/*.sh` orchestrating multi-instance dashboardd + PG | `scripts/chaos/` | P12 drills, then release rehearsals | live PG |
| Security | unit + integration + CI scanners (`scripts/secret-scan.sh`) | per §8 | every CI run; secret scan + RLS fuzz are **release blockers** | mixed |

Unit tests never touch Postgres; integration tests never mock it. Anything asserting an RLS
policy, a partial index, `ON CONFLICT` behavior, `FOR UPDATE` locking, or advisory locks is an
integration test by definition — those behaviors do not exist in fakes.

## 2. Unit tests (offline)

| Package | Key cases |
|---|---|
| `internal/dash/rotation` | Every selection strategy: round_robin fairness (atomic index wraps); **alias-method weighted distribution** — 1M draws over weights {70,20,10} within ±1% absolute of expected (seeded `math/rand/v2`); priority/failover/overflow ordered-walk skips unavailable keys (`atomic.Bool`); **16-bucket re-banding** — key EWMA change moves it to the correct band on the next 1s re-band tick and never on the hot path (injectable clock); region_based sub-ring dispatch; ai_routing posterior update direction; PoolState rebuild on a `config_epochs('platform','key_pool')` bump (asserts the **exact singular** kind literal `key_pool`, per the closed epoch-kind vocabulary {routing_policy, waterfall_workflow, alert_ruleset, provider_catalog, key_pool}) preserves in-flight `Done` callbacks; state-machine transition table (all legal KM-3 edges accepted, all illegal edges rejected with sentinel errors); batched-lease token bucket arithmetic (grant ≤ remaining lease, batch ≤64) |
| `internal/dash/secrets` | AES-256-GCM **Seal/Open against NIST CAVP GCM vectors** (AES-256, 96-bit nonce set: known key/nonce/plaintext/AAD → expected ciphertext+tag, and decrypt-side vectors incl. tag-failure cases); Seal→Open round-trip property test; AAD binding — envelope id or kind swapped → Open fails; wrong master_key_id → sentinel error; `Rotate` re-wraps only `dek_wrapped` (ciphertext bytes unchanged) and stamps `rotated_from`; keyed fingerprint = HMAC-SHA256(pepper, plaintext), differs across peppers; `Secret` type redacts in `String()`, `MarshalJSON`, and `fmt` verbs (`%v`, `%+v`) |
| `internal/dash/security` | **TOTP against RFC 6238 Appendix B vectors** (SHA-1 vector set adapted to our 30s step; time values 59, 1111111109, …, 20000000000); ±1 time-step skew accepted, ±2 rejected; code single-use within a step; **pbkdf2 password hashing against published PBKDF2-HMAC-SHA256 vectors** + golden `algo$iters$salt$dk` format round-trip (600k iterations parameter honored, constant-time compare); recovery-code hash verify + single-use; session id entropy (256-bit, base64url) and idle/absolute expiry arithmetic with injectable clock |
| `internal/dash/db` | **Cursor codec round-trip** for every list key shape (`{k,id}` base64url JSON); **tamper cases**: flipped byte, truncated, non-base64, valid base64 of wrong JSON shape, oversized cursor — all return a typed `invalid_cursor` error, never panic, never leak the decoded struct; bounded-query guard rejects `limit` > 200 or `limit` < 1 as 400 `invalid_filter` (doc 04 §1.4 — server rejects, never clamps; clamping is client-side per doc 08 §4) |
| `internal/dash/audit` | **Hash-chain link/verify**: `hash = sha256(prev_hash ‖ canonical_json)` golden vectors; canonical JSON stability (key order, number formatting); Verify walker detects a mutated row, a deleted row (seq gap), and a re-ordered pair; chain-head advance is monotonic (seq+1 enforced) |
| `internal/dash/keys` | **xlsx reader**: minimal sheet fixture, shared-strings, inline strings, empty cells; **zip-bomb guard** — crafted archive exceeding the decompression-ratio cap rejected before allocation (422), 25MB/50k-row caps enforced; **csv formula-escape**: cells beginning `=`, `+`, `-`, `@`, tab, CR are escaped on ingest and re-escaped on any CSV/NDJSON export (round-trip property: export→reimport is inert); paste/json parsers reject unknown fields (`DisallowUnknownFields`) |
| `internal/dash/cost` | **Forecast math**: least-squares linear fit on synthetic series recovers known slope; 7-day multiplicative seasonality factors from trailing 28d recover a planted weekly pattern; `history_days < 14` → `insufficient_history` with no point array; residual-stddev band widens with noise (band math labeled indicative, UNVERIFIED until backtested); group_by whitelist rejects unknown dimensions (400 `invalid_filter`); credits/NULLIF(successful_results,0) division semantics at zero |
| `internal/dash/alerts` | Evaluator N-of-M bucket breach + empty-bucket policy per metric kind; resolve hysteresis (3 clean evaluations); cooldown renotify arithmetic on `notified_at`; dedupe_key derivation stability; maintenance/paused suppression; `muted_until` skip; budget latch keys (actual once per UTC period, forecast re-arm); cost_anomaly dual threshold (percent AND absolute floor) with top-3 contributor selection |
| `internal/dash/configver` | Validator rule catalog table tests (acyclicity, EXCLUDED Provider, threshold ranges, Cost Ceiling vs budget); tri-state inherit/off/on resolution across all 8 scope-precedence levels (table-driven — most-specific-wins, inherit transparent, effective value + source scope returned); draft edit reverts validated→draft; payload_hash pin |
| `internal/dash/rbac` | Role×action matrix as data: every (role, action) cell asserted; ABAC attribute checks (tenant_id, region, plan tier); deny-by-default for unknown actions |
| `internal/dash/httpx` | Idempotency-Key ledger: replay same key+body → stored result; same key different body → 409; missing key on write → 400 `missing_idempotency_key`; uniform `writeError` shape; `audited()` wrapper emits action/kind |
| `internal/dash/realtime` | Ring buffer: 256-event wraparound, Last-Event-ID lookup, gap detection → `reset` emission; event id format `<epochms>-<seq>` monotonic; QoS split — tick coalescing permitted, `*.changed` never coalesced |

## 3. Integration tests (`-tags integration`, live PG)

### 3.1 RLS cross-tenant zero-rows — EVERY new table (RELEASE BLOCKER)

Extends the `migrations/0001` pattern (Slice 10's `rls_integration_test.go` + `scripts/
run-rls-test.sh`) to all **48 tables** created by migrations 0004–0009 (per doc 03 §3 policy
registry): 25 Class T + 23 Class P. The test runs as non-superuser `app_rls`, binds `app.current_tenant` +
`app.current_role` per transaction through the dual-GUC tx helper, and is a **release blocker**:
a new table without a passing row here fails CI.

For each **Class T** table — seed a row as Tenant A, then assert: (a) Tenant B SELECT → 0 rows;
(b) Tenant B INSERT/UPDATE with Tenant A's tenant_id → blocked by WITH CHECK; (c) operator SELECT
returns rows **only** where an enumerated operator policy exists, else 0 rows:

| Migration | Class T tables (25) | Operator SELECT expected |
|---|---|---|
| 0004 | tenants (bootstrap self-or-operator), users, mfa_recovery_codes, sessions, ip_allowlists, audit_log, audit_chain_heads, api_access_log | tenants ✔, users ✔, audit_log ✔; **sessions ✘, mfa_recovery_codes ✘** (never), ip_allowlists ✘, audit_chain_heads ✘, api_access_log ✘ |
| 0006 | config_versions, config_active, config_epochs, workflow_index | config_versions ✔, config_active ✔, workflow_index ✔; config_epochs ✘ |
| 0007 | alert_channels, alert_rules, alert_events, alert_notifications, approval_policies, approval_requests, approval_decisions | alert_events ✔; all others ✘ |
| 0008 | budgets, bulk_jobs | both ✘ (doc 03 §3: not on the operator-read list); platform-scoped operator bulk jobs run under `tenant_id='platform'`, not an operator policy — assert an operator with a customer-tenant GUC sees 0 bulk_jobs rows |
| 0009 | usage_events, tenant_usage_1h, tenant_usage_1d, cost_rollup_1d | tenant_usage_1h ✔, tenant_usage_1d ✔, cost_rollup_1d ✔; **usage_events ✘** (no operator policy — doc 03 OI-DB-3) |

For each **Class P** table — assert: (a) a customer Tenant principal (any role) SELECT → 0 rows;
(b) customer INSERT/UPDATE/DELETE → blocked; (c) `tenant_id='platform'` principal has full access;
(d) the ONLY exceptions are the two enumerated projections, asserted positively AND negatively:

| Migration | Class P tables (23) | Enumerated tenant projection |
|---|---|---|
| 0005 | providers, secret_envelopes, key_import_batches, key_pools, provider_keys, key_pool_members, key_budgets, health_schedules, rotation_triggers | providers: SELECT only rows with `visibility='tenant_readable'` (catalog fields via view); key_pools/provider_keys: SELECT only rows with `owner_tenant_id = app_current_tenant()` (BYO); **secret_envelopes: none, ever**; health_schedules/rotation_triggers: none (`*_platform_only` ALL policies) |
| 0008 | workers, queue_defs | none |
| 0009 | provider_stats_1m, provider_stats_1h, provider_stats_1d, key_usage_1m, key_usage_1h, key_usage_1d, queue_stats_1m, queue_stats_1h, worker_heartbeats, worker_stats_5m, provider_health_checks, provider_health_1d | none |

Additional assertions: cross-tenant existence is never disclosed (GET of another Tenant's object →
404, not 403); `job_outbox` (0002/0003) remains covered by the existing suite and the dashboard
redrive path re-proves G1 (Tenant A cannot redrive Tenant B's job).

### 3.2 RLS fuzz — Class-P administrative rows (RELEASE BLOCKER)

Property-style fuzz over randomized principals: for R rounds, generate a principal with role ∈
{tenant_admin, tenant_user} and a random non-platform tenant_id, then execute SELECTs against
every Class-P administrative table (secret_envelopes, key_budgets, key_pool_members,
key_import_batches, health_schedules, rotation_triggers, workers, queue_defs, all
provider_stats_*/key_usage_*/queue_stats_*/
worker_heartbeats/worker_stats_5m/provider_health_checks/provider_health_1d) plus providers rows
with `visibility != 'tenant_readable'` and provider_keys/key_pools rows with NULL or foreign
`owner_tenant_id`. **Expected result: zero rows in every round.** Any nonzero result fails the
release. The fuzzer also flips `app.current_role` to 'operator' with a customer tenant GUC and
asserts sessions/mfa_recovery_codes/secret_envelopes still return zero rows (no blanket operator
policy).

### 3.3 Publish atomicity under concurrent publishers

Two goroutines publish different validated versions of the same (tenant, kind, scope_key)
concurrently; exactly one commits, the loser observes 409 `version_conflict`. Invariants polled
during the race from a third connection: config_active always references a version with
`status='published'` whose `payload_hash` matches its pinned validate hash; config_epochs is
bumped exactly once per successful publish; exactly one audit row per publish. A variant edits the
draft between validate and publish and asserts the publish tx re-check rejects (stale-hash 409).

### 3.4 Lease concurrency — no over-lease (`-race`, 50 goroutines)

50 goroutines lease against one Provider Key with `daily_limit = 1000` through
`rotation.LeaseResolver` (batch ≤64) until refused. Assertions: total granted ≤ 1000;
`key_budgets.day_leased ≤ daily_limit` at all times (polled); after nightly reconcile from
synthetic usage_events, `day_used` equals actual completed calls; crash simulation (drop the
in-memory bucket, restart state) over-admits at most one batch (≤64) — the documented bound.
Run under `go test -race`.

### 3.5 Approval exactly-once under concurrent approvers

Request with `required_approvals = 2`; 10 goroutines race decisions (mix of approve/reject,
distinct approver_user_ids, one being the requester — rejected 403). Assertions: quorum counted
under `SELECT ... FOR UPDATE` yields exactly one execution (executor side-effect counter = 1);
Idempotency-Key = request id replays into the stored result; approval_decisions PRIMARY
KEY(request_id, approver_user_id) rejects duplicate votes; expired request (clock advanced past
`expires_at`) refuses decisions in-tx even with the expirer loop stopped.

### 3.6 Concurrency + timing suites

| Test | Setup | Invariant |
|---|---|---|
| Audit chain under concurrent writers | 20 goroutines Append to one Tenant's chain | seq strictly consecutive, every `prev_hash` = predecessor's `hash`, Verify walker green; chain-head row lock serializes without deadlock |
| Heartbeat / lost detection timing | injectable clock; heartbeats stop | `lost` derived at exactly > 3×10s intervals; resumed beats restore `running`; flap inside hysteresis window emits no alert event |
| Drain convergence | worker with jobs_active=3, desired_state→draining | claims stop immediately; status `stopped` only at jobs_active=0; no `job_outbox` row abandoned (all reach terminal or pending) |
| Partition maintenance | injectable clock crossing day/week/month boundaries | tomorrow's usage_events partition pre-created; expired partitions detached per doc 03 §4 retention matrix; inserts never land in the DEFAULT partition after maintenance |
| Redrive idempotency | dead job, double redrive | first UPDATE rowcount 1, second rowcount 0 (no-op); replayed job re-executes without a second charge — G2 idempotency asserted via idempotency_ledger count |
| Config-epoch propagation (two sites) | a providers CRUD/op-state write, then a key_pools strategy write, each polled from a second connection | the providers write bumps `config_epochs('platform','provider_catalog')` and the key_pools write bumps `config_epochs('platform','key_pool')` — each assertion pins the **exact** kind literal (singular `key_pool`, per the closed vocabulary {routing_policy, waterfall_workflow, alert_ruleset, provider_catalog, key_pool}); the resolver cache and PoolState watchers observe the matching bump and rebuild within one tick (incl. the compromise-rotation immediate-rebuild path) |

### 3.7 Aggregator refold determinism

Fold 100k synthetic usage_events → snapshot all rollup rows → truncate rollups → refold → rows
byte-identical (proves `ON CONFLICT ... DO UPDATE SET x = x + EXCLUDED.x` idempotency of the fold
as a unit and licenses the 48h refold-recovery property).

## 4. Contract tests

- **`TestAdminOpenAPIParity`** — mirrors the existing `api/openapi_test.go` pattern against
  `docs/waterfall-dashboard/openapi-admin.yaml`: every route registered on the dashboardd mux
  exists in the spec with matching method, path template, and declared status codes
  (400/401/403/404/409/422/429/500 + 202 where bulk/approval-gated), and every spec operation is
  served by a registered handler. Response bodies of error paths are asserted against the uniform
  envelope `{"error":{"code","message"}}`. Release blocker (extends docs/21 §3).
- **SSE event schema golden files** — one golden JSON per event type: `overview.tiles.tick`,
  `provider.health.changed`, `key.status.changed`, `queue.stats.tick`, `worker.state.changed`,
  `alert.event.fired`, `alert.event.resolved`, `import.batch.progress`,
  `approval.request.changed`. Each emitted event is validated against its golden for the envelope
  `{"v":1,"ts":...,"scope":{...},"payload":{...}}`, snake_case field names, event-name grammar
  `<domain>.<entity>.<verb>` (first segment = topic), and id format `<epochms>-<seq>`. A parity
  check asserts the topic list in the goldens ⊆ server topic registry ⊆ frontend `sse.ts` handler
  map (closed-vocabulary discipline).

## 5. E2E Playwright flows

Run against a dashboardd serving `web/dist` with a seeded live PG. Each flow is a named spec in
`web/tests/e2e/`; all must pass in CI from P11 onward.

| Flow | Steps asserted |
|---|---|
| Login + MFA + recovery code | POST login (pbkdf2) → TOTP prompt → wrong code rejected → correct code → overview; second run uses a recovery code (single-use: reuse rejected); session revocation from /security/sessions logs the browser out |
| Import 1,000-key CSV | /keys/import wizard → upload fixture → 202 job → progress drawer driven by `import.batch.progress` SSE → grid shows 1,000 rows (virtualized; aria-rowcount=1000) with label · ••••last4 · fingerprint prefix; DB-side assertion: 1,000 sealed envelopes, zero plaintext columns |
| Routing publish with approval + rollback | draft edit in dnd-kit editor → client validation mirrors server → POST validate (report rendered) → publish → 202 approval banner → second user approves with X-MFA-Code → config_active flipped, epoch bumped → version rail rollback to prior version (same approval path) → active pointer restored |
| DLQ redrive | seed a poison job to dead; /dead-letters drawer shows payload + last_error + attempts → redrive → job reaches succeeded; double-click redrive is a no-op |
| Alert rule fire→notify→ack | create channel (test-send OK against local sink) → create rule with low threshold → seed breaching rollup rows → episode fires (`alert.event.fired` SSE renders badge) → notification recorded in alert_notifications → ack suppresses renotify → recovery resolves episode |
| RBAC deny-matrix spot checks | tenant_user: no user-management UI, write actions 403 with uniform envelope; tenant_admin: no cross-tenant data, no operator views; operator: cross-tenant read views render AND corresponding audit_log rows appear; direct URL navigation to forbidden routes redirects/denies (server authoritative) |

## 6. Load tests — UNVERIFIED → measured conversion plan

Every quantitative design target below is **UNVERIFIED** until its harness records a number in
P12; results are written back to this table and to doc 00's UNVERIFIED register (repo
discipline: measurement converts the tag, nothing else does).

| # | Claim (UNVERIFIED design target) | Harness / command | Pass threshold |
|---|---|---|---|
| L1 | Key selection ≥10k selections/s per pool, O(1), concurrency-safe | `go test -bench=BenchmarkPoolSelect -benchtime=10s -cpu=1,4,8 ./internal/dash/rotation`; correctness separately via `go test -race -run TestPoolSelectConcurrent` (benchmarks are not run under `-race`) | ≥10,000 ops/s per pool at -cpu=8 across all strategies; zero race reports |
| L2 | SSE fan-out: 200 clients (P7 gate), ≤2s delta latency; extended P12 soak at 500 clients (doc 00 §8 U-4) | `go run ./scripts/load/sse_soak.go -clients 200 -topics overview,key,queue -duration 10m` against 1 dashboardd instance; P12 extended soak reruns with `-clients 500`; measures publish→receipt per event | p99 tick-to-receipt ≤ 2s at both 200 and 500 clients; zero dropped `*.changed` events; reconnect storm (kill/restart mid-soak) recovers via Last-Event-ID or reset within 30s |
| L3 | Import 50k rows within caps | `go test -tags integration -run TestImportLoad50k ./internal/dash/keys` (50k-row CSV fixture ≤25MB) | job completes; 50k envelopes sealed; peak dashboardd RSS bounded (recorded, budget set at first measurement); no plaintext persisted; per-row errors reported not fatal |
| L4 | Rollup fold under 1M synthetic usage_events | `go test -tags integration -run TestFold1M ./cmd/dashboardd` — seeds 1M events across 48h partitions, runs the leader fold, then refolds | fold completes within one retention window (measured duration recorded); refold byte-identical (§3.7 invariant at scale); fold lag metric stays below alert threshold |
| L5 | Admin API p95 under sustained load | `go run ./scripts/load/api_load.go -rps 200 -duration 5m -mix reads=90,writes=10` (list endpoints paginate at limit 200; writes carry Idempotency-Key) | p95 ≤ 250ms, p99 ≤ 750ms at 200 rps (targets UNVERIFIED; recorded values become the doc 11 capacity-model inputs); zero 5xx; error bodies uniform |

Conversion protocol: each row's measured result is committed in P12 with the harness output
attached to the phase commit; a miss opens a remediation item in doc 12 §5 evidence — it is never
silently accepted.

## 7. Chaos tests

Executed as scripted drills (`scripts/chaos/`) against ≥2 dashboardd instances + live PG.

| Drill | Injection | Invariants asserted |
|---|---|---|
| Aggregator leader kill mid-fold | `kill -9` the advisory-lock holder between fold batches | another instance acquires `pg_try_advisory_lock(hashtext('dash_aggregator'))` within one tick; **no double-fold** — re-processing the same events lands on the additive upsert idempotently (§3.7), rollup totals equal single-fold totals |
| dashboardd kill during publish | `kill -9` inside the publish window (fault-point hook between UPDATE and commit) | transaction atomicity: config_active either old or new pointer, never a non-validated or half-written state; epoch and audit row present iff pointer flipped; retry with same Idempotency-Key completes cleanly |
| PostgreSQL restart | restart PG under live SSE clients + API traffic | `internal/pg` pools evict broken conns and reconnect; `/readyz` degrades then recovers; SSE clients resubscribe with Last-Event-ID (or reset) and converge; no lease over-admission beyond the ≤64 documented batch bound |
| Poison import row | CSV with one malformed/oversized/formula row among 10k | row-level failure recorded in `key_import_batches.errors`; remaining 9,999 succeed; job terminal state `partial` (doc 04 §4.1), not crash; drawer renders per-row errors |
| Instance kill mid-bulk-import (doc 11 §4.3 L7) | `kill -9` the dashboardd instance whose `claimed_by` matches the running 50k-import job (L3 harness); rerun as blue-green drain variant (SIGTERM at drain deadline, doc 11 §6 step 4) | janitor (`dash_bulk_janitor`) re-queues the job within one lease interval + one sweep; another instance resumes from the last committed row offset; zero duplicate sealed envelopes (G2); job reaches resumed or terminal state — never stranded `running`; `bulk_jobs_one_in_flight_uq` releases (post-terminal resubmit → 202, not 409 `bulk_job_conflict`); `dash_bulk_jobs_stuck` returns to 0 |

## 8. Security testing

| Suite | Content | Gating |
|---|---|---|
| Secret scan | `scripts/secret-scan.sh` in CI over the full tree **including `testdata/` fixtures and goldens**: pattern + entropy scan for vendor-key shapes, `DASH_MASTER_KEY` values, pepper values, TOTP seeds; plus a runtime assertion that captured slog output and API/audit JSON from the §2/§3 suites contains no seeded plaintext (the `Secret` wrapper proof) | **release blocker** |
| CSRF negative | every mutating `/v1/admin` route without the CSRF header → 403 `{"error":{"code":"csrf_required","message":"..."}}`; header from another session rejected; safe methods unaffected; SSE stream (GET) requires no CSRF but requires the session cookie | CI |
| Session fixation | session id issued pre-login is never promoted: id rotates at login AND at MFA verify; the old id is dead (401) after rotation; `mfa_verified_at` never carries over; cookie flags asserted (HttpOnly, Secure, SameSite per ADR-0018) | CI |
| IP allowlist bypass | with an allowlist configured: direct connection from a non-allowed IP → 403; spoofed `X-Forwarded-For` / `Forwarded` headers from an untrusted hop do not bypass (only the configured proxy depth is honored); IPv6-mapped-IPv4 and CIDR edge encodings tested (mirrors the docs/21 SSRF-corpus discipline applied inbound) | CI |
| Formula injection round-trip | import cells starting `=`, `+`, `-`, `@`, tab, CR → stored escaped; NDJSON/CSV exports re-escape; export→reimport is inert (no formula survives a full cycle); UI copy paths (clipboard) copy envelope ids, never secrets | CI |
| SSRF on notifiers | alert channel targets resolving to the 17-address internal corpus (metadata, RFC1918, loopback, ULA, link-local, CGNAT, 0.0.0.0/8, IPv4-mapped) blocked for BOTH real send and test-send; redirects refused; response size capped | CI |
| RLS negative | §3.1 zero-rows (all 48 tables) + §3.2 fuzz — **restated: these are release blockers**, extending the docs/21 §3 CI-gate list (G1 negative isolation, G2 replay, secret scan, OpenAPI contract) with the dashboard's tables and the dual-GUC role dimension | **release blocker** |

## Open items

| ID | Item | Status | Owner |
|---|---|---|---|
| OI-TS-1 | NIST CAVP GCM vector subset selection (AES-256, 96-bit nonce; encrypt + decrypt/tag-fail files) to vendor into `internal/dash/secrets/testdata/` | OPEN (P1) | Senior Backend Engineer |
| OI-TS-2 | PBKDF2-HMAC-SHA256 public vector source pinned (RFC 6070 is SHA-1; use the widely mirrored SHA-256 vector set + repo golden vectors) | OPEN (P0) | Senior Backend Engineer |
| OI-TS-3 | L3/L4/L5 absolute thresholds (RSS budget, fold duration, rps target) are set at first measurement in P12 and written back here | OPEN (closes in P12) | Senior Backend Engineer |
| OI-TS-4 | Playwright browser matrix (Chromium-only in CI vs +Firefox/WebKit) and screenshot-diff tolerance | OPEN (P8) | Enterprise UX Architect |
| OI-TS-5 | Fault-point hook mechanism for the publish-crash drill (env-gated test hook vs debug endpoint) — must not exist in production builds | OPEN (P12) | Senior Backend Engineer |
