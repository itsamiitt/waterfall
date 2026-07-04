# ADR 0020 — Platform/tenant table taxonomy: Class P/T/R, sentinel platform tenant, dual GUC, FORCE RLS everywhere

- **Status:** Accepted
- **Date:** 2026-07-02
- **Deciders:** Senior Backend Engineer, Staff Security Engineer, Principal Product Architect
- **Phase:** Dashboard P0 · **Source:** `docs/waterfall-dashboard/03-database-schema.md`

## Context
Migrations 0001–0003 established the RLS pattern (ADR-0011): every table carries
`tenant_id text NOT NULL`, `ENABLE` + `FORCE ROW LEVEL SECURITY`, policy
`USING (tenant_id = app_current_tenant())`, tenant bound per transaction from the verified
`tenant.Principal`, app role `app_rls` with **no BYPASSRLS**. The dashboard (migrations 0004–0009) adds
tables that break the "every row has a natural Tenant" assumption: the Provider catalog,
Provider Keys and Key Pools, `secret_envelopes`, workers, queue definitions, and platform-wide
telemetry rollups are platform infrastructure, not tenant data — yet some need narrow tenant-visible
projections (catalog browsing, tenant-owned BYO Key Pools). Separately, operators legitimately read
across tenants for enumerated views (cost rollups, audit logs), which G1 tenant isolation requires to
be "explicit, enumerated, always audit-logged" — never ambient. The question: how do platform-scoped
tables and operator cross-tenant reads fit the single RLS mechanism without weakening it?

## Options considered
| Option | Pros | Cons | Key tradeoff surfaced |
|--------|------|------|-----------------------|
| **A. Sentinel `platform` tenant + second GUC `app.current_role` (chosen)** | ONE audited code path — the same tx helper sets both GUCs for every query; FORCE RLS stays on **every** table including platform tables; no NULL semantics anywhere; cross-tenant access is a finite, reviewable list of `FOR SELECT` policies; one-owner-per-table stays auditable | `'platform'` becomes a reserved tenant id that signup must never mint; policies are more verbose than "no RLS" | uniformity of the isolation mechanism vs a small amount of policy ceremony |
| B. Separate Postgres schema for platform tables, RLS off there | conceptually tidy split; fewer policies | **rejected: a second code path** — some queries run under RLS and some do not, so the tx helper, tests, and reviewer intuition all fork; the uniform-RLS story ("every table is FORCE RLS, no exceptions") weakens exactly where the most sensitive rows live; the zero-rows test suite no longer covers everything | schema aesthetics vs a single provable isolation mechanism |
| C. Nullable `tenant_id` with policy `tenant_id IS NULL OR tenant_id = app_current_tenant()` | no new tenant row | **rejected: NULL-policy footguns** — SQL three-valued logic makes NULL comparisons silently drop or admit rows depending on policy phrasing; the `NOT NULL` invariant from ADR-0011 is lost; every future policy author must reason about NULL correctly forever | avoiding one seed row vs a permanent class of subtle policy bugs |
| D. Operator role with BYPASSRLS | trivially powerful | **rejected outright: violates ADR-0011's no-BYPASSRLS invariant** for application roles (only the relay role holds it); grants ambient, unenumerated cross-tenant power to a human-facing role — the classic control-plane breach multiplier, and unauditable at the policy layer | none; not a viable option |

## Decision
**Option A — three table classes, one mechanism.** Keep `app.current_tenant`; add a second GUC
`app.current_role` (values `operator`/`tenant_admin`/`tenant_user`). Both are bound per transaction by
the `internal/dash/db` tx helper from the verified `Principal` — role derived from the session or JWT
scope `role:<r>` (ADR-0018), **never from request bodies**. Seed one reserved row:
`tenants('platform', kind='platform')`; the `kind` CHECK admits only `platform`/`customer`, and signup
creates only `kind='customer'`.

- **Class P — platform tables** (no `tenant_id` column): `providers`, `provider_keys`, `key_pools`,
  `key_pool_members`, `key_budgets`, `secret_envelopes`, `provider_health_checks`, `provider_stats_*`,
  `key_usage_*`, `workers`, `worker_heartbeats`, `queue_stats_*`, `queue_defs`. **FORCE RLS** with
  policy `USING (app_current_tenant() = 'platform')` for ALL commands, plus explicitly enumerated
  tenant read-projections only where needed: `providers` gains
  `FOR SELECT USING (visibility = 'tenant_readable')` (catalog fields only, via views);
  `provider_keys`/`key_pools` gain `FOR SELECT USING (owner_tenant_id = app_current_tenant())` for
  tenant-owned BYO Key Pools; **`secret_envelopes` carries no tenant policy ever** — operator path
  only, read only by the secrets backend (ADR-0017).
- **Class T — tenant config tables** (`tenant_id NOT NULL`, 0001-style policy): `tenants` (bootstrap
  policy `USING (id = app_current_tenant() OR app_current_role() = 'operator')`), `users`, `sessions`,
  `mfa_recovery_codes`, `ip_allowlists`, `audit_log`, `audit_chain_heads`, `api_access_log`,
  `config_versions`, `config_active`, `config_epochs`, `workflow_index`, `budgets`, `alert_channels`,
  `alert_rules`, `alert_events`, `approval_policies`, `approval_requests`, `approval_decisions`,
  `usage_events`, `tenant_usage_*`, `cost_rollup_*`. Platform-scoped rows simply use `tenant_id='platform'`.
  **Operator cross-tenant SELECT policies** (`USING (app_current_role() = 'operator')`) exist ONLY on:
  `cost_rollup_*`, `tenant_usage_*`, `audit_log`, `alert_events`, `config_versions`/`config_active`
  (read), `workflow_index`, `users`, `tenants` — and **every handler serving a cross-tenant operator
  view writes an `audit_log` row**. There is NEVER a blanket operator policy on `sessions`,
  `secret_envelopes`, `mfa_recovery_codes`, `usage_events` (the aggregator folds it per Tenant — see
  below), or the G2 idempotency / G4 cost ceiling / G5 provenance ledgers. The member lists here are
  the decision's shape; the authoritative per-table policy registry lives in
  `docs/waterfall-dashboard/03-database-schema.md` and is diffed against `pg_policies` in CI.
- **Class R — telemetry/rollups:** RANGE-partitioned by time, written ONLY by the leader-elected
  aggregator (`pg_try_advisory_lock(hashtext('dash_aggregator'))`), read through bounded windows under
  the bounded-query guard (cursor pagination, limit cap 200). Partition create/detach runs as a runtime
  maintenance job, not migrations.

Background jobs with no request `Principal` still flow through the same dual-GUC tx helper, but bind
it in two distinct ways:
- **Sunset sweep, approval expirer:** platform tenant + operator role, and an `audit_log` row for
  every cross-tenant read — exactly like operator handlers.
- **Leader aggregator** (single writer under `pg_try_advisory_lock(hashtext('dash_aggregator'))`):
  the operator binding cannot work here — `usage_events` deliberately carries no operator SELECT
  policy, and the operator policies on `tenant_usage_*`/`cost_rollup_1d` are SELECT-only (their
  tenant-isolation `WITH CHECK` blocks platform-tenant writes of customer rows). Instead the
  aggregator iterates the Tenant list (operator-readable `tenants`) and folds each Tenant's events in
  a per-Tenant transaction with `app.current_tenant` bound to that Tenant: Class T rollups
  (`tenant_usage_*`, `cost_rollup_1d`) are written inside that Tenant's transaction, while Class P
  rollups (`provider_stats_*`, `key_usage_*`, `queue_stats_*`) accumulate in memory across the pass
  and are written under `app.current_tenant = 'platform'` (doc 03 §9.4, OI-DB-3). No operator
  INSERT/UPDATE policies exist on any table.

## Rationale
G1 tenant isolation is only as strong as its weakest code path, so the deciding force is **uniformity
over convenience**: Option A is the only design where "every table has FORCE RLS and every query flows
through one tx helper" remains literally true, which is what makes the cross-tenant zero-rows test
suite a complete proof rather than a sample. Options B and C each reintroduce a category of reasoning
("is this table special?") that policy authors and reviewers must get right forever; Option D deletes
the invariant outright. The sentinel-tenant cost — guarding one reserved id — is a CHECK constraint and
a signup guard, paid once. This is the governing invariant applied to the database: **the model
proposes, a deterministic gate disposes** — RLS policies are the deterministic gate, and the enumerated
operator SELECT list plus mandatory audit rows keep cross-tenant power explicit, finite, and observable.

## Consequences
- Positive: G1 enforcement is uniform and mechanically testable across all ~40 new tables; operator
  power is a reviewable policy list, not a role attribute; the dual-GUC helper gives every feature
  package identical tenancy semantics; one-owner-per-table auditing survives.
- Negative / accepted costs: `'platform'` is a reserved identifier guarded by CHECK + signup logic;
  the dual-GUC tx helper is a hard dependency of every dashboard query (fail-closed: missing Principal
  = no GUCs = zero rows); enumerated policies mean genuine new operator views require a migration plus
  review — deliberate friction; policy verbosity grows the migration files.
- Follow-ups / new ADRs triggered: per-table policy registry maintained in
  `docs/waterfall-dashboard/03-database-schema.md`; ADR-0017 (secret_envelopes policy stance) and
  ADR-0018 (role scope binding) compose with this ADR; any future BYPASSRLS request must supersede
  ADR-0011 — this ADR does not create an exception path.

## Verification
Release-blocking integration suite: for EVERY new table, cross-tenant reads return **zero rows** under
a customer-tenant principal, and Class P writes fail under any non-platform principal; operator
cross-tenant endpoints are asserted to write `audit_log` rows (hash-chain verified); a signup test
proves `kind='platform'` cannot be created through any API path; RLS fuzz cases (P12 security pass)
mutate GUC combinations and expect fail-closed behavior; the doc-03 policy registry is diffed against
`pg_policies` in CI so documented and deployed policies cannot drift.
