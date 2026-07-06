# 15 — Pending-Completion Plan

> **Status:** DRAFT · **Owner:** Solutions Architect · **Last updated:** 2026-07-06 · **Gated by:** /architecture-review, /security-audit

Detailed implementation plan for every task still pending after the P0–P12 build and the post-P12
closeout. Decisions taken (2026-07-06): deferred infra = **plan + deploy-steps only** (no backend
code until provisioned); SEC-5 = **per-tenant admin `require_mfa` knob**; SEC-3 = **operator
provisioning API**; optional items = **all four selected** (residual bundle, bulk-cancel, cost
`key_id`, Firefox+WebKit Playwright).

Two new migrations and two new ADRs are introduced:
- **`migrations/0012_dash_provisioning_mfa.sql`** — `tenants.require_mfa`, `tenant_invites`,
  `bulk_jobs` `cancelled` status, `cost_rollup_1d.key_id`.
- **ADR-0021** — operator Tenant provisioning path (target-Tenant-bound INSERT, no BYPASSRLS).
- **ADR-0022** — per-store adapter dependency policy (which design-target backends may take a
  third-party client and under what gate).

Everything in Part 1 is **build-now** (verifiable on the current Go 1.26 + PG17 + Node box). Part 2
is **plan-only** (design + deploy runbook). Part 3 is the **staging** load/chaos plan.

---

## Part 1 — Build-now tasks

Ordered by dependency. Each task: **Goal · Schema · Files · Approach · Acceptance**.

### T1 — Operator Tenant provisioning API (SEC-3, ADR-0021)  — size L

**Goal.** Replace the out-of-band `dashseed` step with a controlled, audited operator path that
creates a customer Tenant + its first `tenant_admin` + a one-time setup token, without granting the
app role `BYPASSRLS`.

**Schema (0012).**
```sql
CREATE TABLE tenant_invites (
    id          uuid PRIMARY KEY,
    tenant_id   text NOT NULL REFERENCES tenants(id),
    email       text NOT NULL,
    role        text NOT NULL CHECK (role IN ('tenant_admin','tenant_user')),
    token_hash  bytea NOT NULL,          -- sha256 of the emailed 256-bit token; never store plaintext
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_by  uuid,                    -- provisioning operator
    created_at  timestamptz NOT NULL DEFAULT now()
);
-- Class T, tenant-isolated; the accept-invite path is token-authenticated (pre-session), so it reads
-- the row via a dedicated token lookup bound to the invite's own tenant_id.
```

**Files.** New `internal/dash/provisioning/{service.go,store.go,http.go,invite.go,*_test.go}`;
`internal/dash/httpx/server.go` mounts `POST /v1/admin/tenants` and the public
`POST /v1/admin/auth/accept-invite`; `cmd/dashboardd/main.go` wires the service; ADR-0021;
`openapi-admin.json/.yaml` + apispec parity; `web/src/features/security/` provisioning form
(operator-only).

**Approach.**
- **The RLS trick (ADR-0021):** the operator's RBAC (operator role + MFA step-up) is checked in the
  handler; the DB tx then binds `app.current_tenant = <new tenant id>` (not `platform`) so the
  `tenants` and `users` `WITH CHECK (… = app_current_tenant())` policies pass for the *new* Tenant.
  No BYPASSRLS. The tenant id is validated against the slug CHECK and must not already exist. All in
  one tx: insert `tenants`, insert the first `users` row (status `invited`, no password), insert a
  `tenant_invites` row with a hashed 256-bit token, audit under the new tenant.
- `POST /v1/admin/tenants {id,name,plan_tier,admin_email}` → 201 `{tenant_id, invite_token}`
  (operator-only, `X-MFA-Code` step-up, Idempotency-Key). The token is returned once (and/or emailed
  via the alert-notifier SMTP path).
- `POST /v1/admin/auth/accept-invite {token,password}` (public): look up by `token_hash`, check
  unexpired/unused, set the user's `password_hash` (pbkdf2), mark `used_at`, optionally start a
  session. MFA enrollment then follows the normal flow (and T2's `require_mfa`).
- Retire `cmd/dashseed` in favour of this path (keep it as a dev convenience but document the API as
  the sanctioned route).

**Acceptance.** Integration: operator provisions Tenant `acme` + admin; a customer principal cannot
provision (403); accept-invite sets the password and the admin can log in; the invite is single-use
(second accept → 409); RLS zero-rows still holds for `tenant_invites`; apispec parity 147==147.

### T2 — Per-Tenant require_mfa knob (SEC-5)  — size M

**Goal.** Let a `tenant_admin` require MFA for all their Tenant's users (default off).

**Schema (0012).** `ALTER TABLE tenants ADD COLUMN require_mfa boolean NOT NULL DEFAULT false;`
(additive, online-safe).

**Files.** `internal/dash/security/users.go` (+ a `Tenants` read for the flag) or a small
`internal/dash/security/tenantpolicy.go`; `internal/dash/httpx/auth_handlers.go` (login enforcement);
a `PATCH /v1/admin/settings/mfa-policy` (tenant_admin) handler; `web/src/features/security/`
settings toggle; tests.

**Approach.**
- Login flow: after password verification, read `tenants.require_mfa` for the user's Tenant. If true
  and the user has no `mfa_totp_envelope_id`, return `status:"mfa_enrollment_required"` (a new
  documented status) and route the SPA to the enrollment page — the session is not fully established
  until enrolled + verified. If enrolled, the existing `mfa_required` step-up path is unchanged.
- `PATCH .../settings/mfa-policy {require_mfa:true}` — tenant_admin only, audited, MFA step-up.
- operator/tenant_admin already require MFA today; this only tightens `tenant_user`.

**Acceptance.** Integration: with `require_mfa=true`, a not-enrolled `tenant_user` login →
`mfa_enrollment_required`, and cannot reach any `/v1/admin` route until enrolled+verified; with
`false`, login proceeds as today; the toggle is audited and RBAC-gated.

### T3 — Bulk-job cancellation (OI-API-4)  — size M

**Goal.** Cancel an in-flight bulk job (import / replay / rolling-restart) to a clean terminal state.

**Schema (0012).**
```sql
ALTER TABLE bulk_jobs DROP CONSTRAINT bulk_jobs_status_check;   -- widen the enum
ALTER TABLE bulk_jobs ADD CONSTRAINT bulk_jobs_status_check
    CHECK (status IN ('queued','running','succeeded','partial','failed','cancelled'));
ALTER TABLE bulk_jobs ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false;
```
(Small config table, applied pre-traffic-safe; for a live ALTER use the 0010+ expand→contract rule.)

**Files.** `internal/dash/queues/{http.go,pgstore.go,replay.go}` (cancel endpoint + executor
cooperation), `internal/dash/keys/bulkjobs.go` (import loop honours cancel), `openapi-admin` +
apispec, `web/src/features/keys|queues` progress-drawer Cancel button.

**Approach.**
- `POST /v1/admin/bulk-jobs/{id}/cancel` (RBAC per kind; audited) sets `cancel_requested=true`.
- Each executor (keys import loop; queues replay loop; workers rolling-restart wave loop) checks
  `cancel_requested` between rows/waves; on cancel it stops claiming further items, records the
  partial result, and transitions to `cancelled` (rows already committed stay committed — G2/idempotency
  means a resubmit is safe). The janitor already unwedges the one-in-flight index for terminal states.

**Acceptance.** Integration: cancel a running 5k import mid-run → terminal `cancelled`, committed
rows retained, `cancel` idempotent, no crash; cancelling a finished job → 409/no-op.

### T4 — key_id in cost_rollup_1d (RF-3)  — size M

**Goal.** Key-scoped cost drill-down beyond the 48h `usage_events` window.

**Schema (0012).** Add `key_id text` to `cost_rollup_1d` and widen its upsert key to include it:
```sql
ALTER TABLE cost_rollup_1d ADD COLUMN key_id text NOT NULL DEFAULT '';
-- recreate the ON CONFLICT unique to (tenant_id, provider_id, workflow_key, country, key_id, day)
```
Cardinality note: `key_id` multiplies daily rows by active-keys-per-(provider,workflow); bounded by
the same 2y retention + monthly partition drops. Documented in doc 03 §4.

**Files.** `migrations/0012`, `internal/dash/telemetry/aggregator.go` (fold includes `key_id`),
`internal/dash/cost/{query.go,service.go}` (`group_by=key` served from `cost_rollup_1d`), doc 03 §2.6
+ §4, tests (fold refold-identity with the new dim; group-by-key totals == ledger).

**Approach.** The aggregator already carries `key_id` in `usage_events`; include it in the
`cost_rollup_1d` fold key. Backfill is unnecessary (rollups refold from `usage_events` within its 48h
window; older days keep `key_id=''` = "unattributed", documented). The cost query builder adds `key`
to its group-by whitelist over `cost_rollup_1d` (previously it fell back to the 48h window).

**Acceptance.** Fold refold-identity holds with the new dim; `GET /cost/summary?group_by=key` over a
multi-day window returns per-key credits matching the seeded ledger; retention/cardinality noted.

### T5 — Residual completions bundle  — size L (five sub-items)

**T5a — enrichd drain-gating (OI-P5-2).** Gate job admission on the heartbeat client's desired
state. Files: `cmd/enrichapi/main.go`, `internal/api/server.go`. Approach: pass the heartbeat
`Client` (or a `ShouldClaim() bool`) into `api.Server`; the submit handler returns `503
{"error":{"code":"draining"}}` with Retry-After when `!ShouldClaim()`; the relay/dispatcher stops
pulling new work; in-flight jobs finish (they hold leased keys + reserved credits). Test: set
`desired_state=draining` via the heartbeat ack → new submits 503, in-flight complete, worker reaches
`stopped` at `jobs_active=0`.

**T5b — bulk-job auto-resume (OI-KEYS-1c).** Add an execution poller so a re-queued bulk job runs on
a survivor. Files: `internal/dash/queues/{runner.go}`, wire in `cmd/dashboardd`. Approach: the
janitor, for *resumable* kinds (import/replay/rolling_restart), re-queues (status→`queued`, clear
claim) instead of failing; a `BulkJobRunner` loop (claim `queued` rows `FOR UPDATE SKIP LOCKED` under
a lease) resumes execution **from `succeeded`** (rows commit independently, so it continues past the
last committed item; G2 makes re-attempted rows idempotent). Non-resumable/exhausted-attempts →
terminal `failed`. Test: kill the executor mid-import (lease expires) → the runner reclaims and
completes to `succeeded`/`partial`, total rows correct, no double-charge.

**T5c — customer workflow/country usage attribution (OI-P4-1b).** Thread `workflow_key`/`country`
into the rotation lease context so `Lease.Done` records them. Files: `internal/engine/engine.go` (or
the egress call site) sets ctx values before `provider.Call`; `internal/dash/rotation/{lease.go,
usage.go}` reads them. Approach: define ctx keys `rotation.WithAttribution(ctx, workflowKey,
country)`; the engine sets them from the `EnrichmentRequest`; rotation captures at `Lease()` and
emits on `Done()`. Populates once enrichd routes real traffic through leases (dashboard-initiated
calls stay `platform`/empty). Test: a lease drawn under an attributed ctx records the workflow/country
on the usage row.

**T5d — full same-tx audit (SEC-7).** Make `audited()` append in the business write's tx. Files:
`internal/dash/httpx/{audited.go}`, and the mutating feature handlers. Approach: handlers that open a
`db.Store` tx put the `*pg.Conn` on the context (`httpx.WithAuditConn`); `audited()` uses
`audit.AppendConn(ctx, conn, …)` when present, else the current follow-up-tx path. Retrofit the
highest-value mutating handlers (provider/key/config writes) first; the fallback keeps chain-verify
correct meanwhile. Test: a business write and its audit row commit/rollback atomically (inject a
post-write failure → neither persists).

**T5e — recovery-code on step-up.** `totpStepUp.VerifyStepUp` accepts a recovery code. Files:
`cmd/dashboardd/main.go` (or move the verifier into `internal/dash/security`). Approach: try
`VerifyAndConsume(TOTP)`; on miss, try `ConsumeRecoveryCode`; either success passes. Test: a step-up
with a valid recovery code succeeds and consumes it (single-use).

### T6 — Playwright Firefox + WebKit (OI-TS-4)  — size S

**Goal.** Run the live E2E on three engines with a screenshot-diff tolerance.

**Files.** `web/playwright.config.ts` (projects: chromium/firefox/webkit; `maxDiffPixelRatio`),
`web/package.json` (`e2e:all`), doc 13 §5. Approach: `npx playwright install firefox webkit`; add the
three projects; keep the E2E env-gated (needs a live dashboardd + seeded user, now via T1's
provisioning API). Acceptance: `npm run e2e:all` passes on all three engines against a booted
dashboardd; screenshot diffs within tolerance.

### Build-now sequencing & gate

`0012` migration first (T1–T4 schema) → T1 provisioning → T2 MFA knob (uses T1's tenants) → T3
cancel → T4 cost key_id → T5 residuals (independent; parallelizable across disjoint packages) → T6
Playwright (uses T1 to seed). New ADRs 0021/0022 before their code. Each task gates on
`go build/vet/test` + `-race` (touched concurrency) + the live integration suite + apispec parity;
T6 on the browser matrix. One commit per task, `feat(dash): …`.

---

## Part 2 — Plan-only: design-target backend adapters (ADR-0022)

For each: **the seam today · adapter design · dependency decision · deployment steps · cutover ·
measured trigger**. No code until a real backend is provisioned in staging.

### Redis — SSE fan-out / hot config / breaker state
- **Seam:** `internal/dash/realtime.Source` (poller today); rotation breaker/lease state in-process.
- **Adapter:** a hand-rolled RESP client (stdlib TCP, mirroring `internal/pg`), a `realtime.Source`
  backed by Redis pub/sub for cross-instance fan-out, and a shared breaker/lease KV.
- **Dependency:** hand-rollable stdlib (RESP is simple) — no ADR-0016 exception needed. ADR-0022
  records it as the one adapter buildable without a third-party dep.
- **Deploy:** managed Redis (ElastiCache/Memorystore) in the cell; `DASH_REDIS_URL`; TLS + AUTH.
- **Cutover:** dual-run (poller stays the fallback behind the `Source` interface); flip per-instance
  via env; validate fan-out latency, then retire the poller for that cell.
- **Trigger:** SSE fan-out saturation (measured `dash_sse_clients` beyond a single instance's soak
  ceiling) or multi-instance tick-latency > 2s.

### ClickHouse — analytics rollups at volume
- **Seam:** telemetry rollups on Postgres partitioned tables; the cost/overview read helpers.
- **Adapter:** a `telemetry.RollupStore` implementation writing/reading ClickHouse (CDC from
  `usage_events` or dual-write), keeping the same query shapes.
- **Dependency:** needs a ClickHouse client → **ADR-0016 exception (ADR-0022)**. HTTP interface is
  hand-rollable (ClickHouse speaks HTTP+SQL) — prefer that to avoid a driver dep.
- **Deploy:** managed ClickHouse; CDC pipeline (Debezium/logical) or an aggregator dual-write.
- **Cutover:** shadow-read (compare CH vs PG rollups) → flip reads → drop PG rollup tiering.
- **Trigger:** PG rollup volume/latency breach (doc 11 §4 `key_usage_1m` row-count model exceeded).

### Kafka / Redpanda — distributed transport (ADR-0013)
- **Seam:** `job.Submitter` / the pgoutbox relay; `queues.QueueBackend` read model is already
  engine-agnostic (QS-TMP-1).
- **Adapter:** a Kafka-backed `Submitter` + a consumer relay; partition = tenant_id.
- **Dependency:** a Kafka client is large → **ADR-0016 exception**. Redpanda (Kafka-protocol)
  preferred.
- **Deploy:** managed Redpanda; per-stage topics + DLQ; consumer-lag autoscaling (doc 11).
- **Cutover:** run the transactional-outbox path and Kafka in parallel; migrate one stage at a time;
  the queues/workers panels are already engine-agnostic.
- **Trigger:** the costed Action-volume spike of QS-TMP-1 / cross-region transport need.

### Temporal — durable orchestration (ADR-0014, cost-gated)
- **Seam:** the hand-rolled saga + transactional outbox is the accepted fallback; the orchestrator
  "plans only".
- **Adapter:** Temporal workflows/activities for the waterfall; the queue/worker panels stay
  engine-agnostic.
- **Dependency:** Temporal SDK → **ADR-0016 exception**; big stateful service (tension with the
  modulith, doc 10 QS-TMP-1).
- **Deploy:** Temporal cluster per cell; task-queue fairness for whale tenants.
- **Cutover:** shadow a subset of workflows; promote after offline replay parity.
- **Trigger:** the same QS-TMP-1 human gate.

### S3 Object-Lock — WORM audit anchor (SEC-2/DS-3)
- **Seam:** the per-Tenant audit hash chain + `audit_chain_heads`; `GET /audit-log/verify`.
- **Adapter:** a nightly ops job that exports `audit_chain_heads` + the day's `audit_log` range to an
  Object-Lock (compliance-mode) bucket. **Hand-rollable stdlib** (S3 REST + SigV4) — no dep.
- **Deploy:** a bucket with Object Lock; `DASH_WORM_BUCKET` + IAM; nightly schedule (cron/K8s Job).
- **Cutover:** additive (defense-in-depth); the in-DB chain + verify remain the primary control.
- **Trigger:** compliance requirement at deploy; tail-truncation exposure kept ≤ 24h.

---

## Part 3 — Staging load & chaos (needs a multi-host lab)

Single-instance dev floors are measured (doc 13 §6). The following require staging and are specified
as runnable harnesses to hand to that environment:
- **`scripts/load/sse_soak.go`** — 500 clients / 10 min; assert p99 tick-to-receipt ≤ 2s, zero
  dropped `*.changed`, reconnect-storm recovery ≤ 30s (OI-P12-1/L2).
- **Multi-instance bulk-job resume** — kill the executing instance mid-50k-import; a second instance's
  janitor+runner (T5b) resumes to terminal; the one-in-flight index unwedges (L3 blue-green).
- **PG-failover DR** — primary failover under load; assert pool reconnection, RPO ≤ 5 min / RTO ≤ 1 hr
  (doc 11), SSE resubscribe (OI-RB-4).
- **Aggregator/leader chaos at N instances** — kill the leader mid-fold; assert no double-fold
  (ON CONFLICT idempotency) and one-writer resumes.
Each writes measured numbers back into doc 13 §6 and flips the corresponding UNVERIFIED rows.

---

## Sequencing summary

1. ADR-0021, ADR-0022; migration `0012`.
2. T1 → T2 → T3 → T4 (schema-dependent chain).
3. T5a–e + T6 (parallelizable).
4. Part-2 adapters authored as design sections in this doc's siblings / ADRs (no code).
5. Part-3 harnesses committed as `scripts/load/*` + doc 13 §6 rows, run in staging.

## Open items

| ID | Item | Status | Owner |
|----|------|--------|-------|
| PC-1 | ADR-0022 must decide, per backend, whether the adapter is hand-rolled stdlib (Redis, S3) or takes an ADR-0016 dependency exception (ClickHouse HTTP, Kafka, Temporal) | OPEN — decide at ADR | Solutions Architect |
| PC-2 | T4 `cost_rollup_1d.key_id` cardinality vs retention — confirm the monthly-partition drop keeps row counts bounded at the new grain before shipping | OPEN — verify in T4 | Senior Backend Engineer |
| PC-3 | T1 invite delivery: return-token-once vs SMTP email (reuse the alert-notifier SMTP path) — pick per deployment | OPEN — T1 | GTM Infrastructure Engineer |
