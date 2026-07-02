# 17 — Dashboard Planning

**Status:** `IN-REVIEW` · **Owner:** Senior Product Manager · **Last updated:** 2026-07-01
**Gated by:** [doc-consistency](../skills/doc-consistency/SKILL.md) · [Security Auditor](../agents/security-auditor.md) (RBAC) · `/architecture-review`

> Every panel maps to data/services already planned (no orphan UI — `doc-consistency`). RBAC/ABAC
> governs visibility; **tenant admins see only their tenant (G1)**; operators see cross-tenant ops only.

## 1. Audiences & scope
| Audience | Scope | Auth |
|----------|-------|------|
| **Platform operator** (us) | cross-tenant infra/provider/queue/billing ops | RBAC role `operator` (ABAC on region) |
| **Tenant admin** | their tenant only (RLS-scoped) | RBAC `tenant_admin` |
| **Tenant user** | their usage/results | RBAC `tenant_user` |

## 2. Panels → backing data/service (no orphan UI)
| Group | Panels | Backing (`05`/`06`/`20`) |
|-------|--------|--------------------------|
| **Providers** | provider management, provider health, provider **simulator**, benchmarking, rankings, coverage (email/phone/intent) | key-manager, `provider_statistics`, ClickHouse |
| **Keys & credits** | API keys, credits, quota/limit monitors, auto disable/enable status | key-manager, `provider_keys`, `credits` |
| **Billing & cost** | billing, cost analytics, customer usage, subscription plans, usage limits | cost/billing, `usage`/`billing`, ClickHouse |
| **Jobs & queues** | queues, workers, retry jobs, dead jobs, running jobs, scheduled jobs, job history, export center, webhook logs | Kafka/Temporal, `enrichment_jobs`, DLQ topics |
| **Observability** | logs, audit logs, error logs, performance metrics, latency, success/failure rate, regional analytics, alerts, notifications | Prometheus/Grafana/OTel/ELK (`20`), `audit_logs` |
| **Admin/governance** | maintenance mode, feature flags, RBAC, tenant management | admin API, config-as-data |

**Provider simulator** = dry-run a waterfall plan (which providers, order, expected cost/confidence)
against a sample record using current reservation values — read-only, no paid calls.

## 3. RBAC/ABAC visibility (aligns `18`)
Server-side enforcement (never client-trusted); ABAC attributes: tenant_id, region, plan tier. Operator
cross-tenant views are audit-logged. Every dashboard query is RLS-scoped for tenant audiences.

## 4. Open items
| ID | Item | Status |
|----|------|--------|
| DA-1 | Panel→data mapping | ✅ §2 |
| DA-2 | RBAC/ABAC visibility matrix | ✅ §3 (detail with `18`) |
| DA-3 | Operator vs tenant scope | ✅ §1 |

## 5. Reviewer result
| Check | Result |
|-------|--------|
| Every panel has a backing service/table (no orphan UI) | PASS |
| Tenant-admin views RLS-scoped (G1) | PASS |
| Operator cross-tenant actions audited | PASS |
| All required panels covered | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
