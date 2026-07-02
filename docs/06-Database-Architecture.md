# 06 — Database Architecture

**Status:** `IN-REVIEW` · **Owner:** Database Architect · **Last updated:** 2026-07-01
**Gated by:** [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) (G1,G5) · [Security Auditor](../agents/security-auditor.md) · `/architecture-review` + `/security-audit`

> Engine + isolation model ratified in **[ADR-0011](../adr/0011-datastore-postgres-rls-pool.md)**:
> PostgreSQL RLS-pool + Redis + ClickHouse. ERD: [`diagrams/erd.mmd`](../diagrams/erd.mmd).

## 1. Stores
| Store | Role | Notes |
|-------|------|-------|
| **PostgreSQL** | OLTP system-of-record | RLS-pool; provenance + ledgers + config-as-data |
| **Redis** | cache/coordination | cache/preview, idempotency fast-path, cost reservation, breaker state, concurrency tokens; **tenant-prefixed keys** |
| **ClickHouse** | analytics | api_logs, usage, provider_statistics, history; time+tenant partitioned; via CDC |

## 2. Tenant isolation (G1) — enforced at the database
Every tenant-scoped table carries `tenant_id` and `FORCE ROW LEVEL SECURITY`. The hot-path DB role has
**no `BYPASSRLS`**; each transaction runs `SET LOCAL app.current_tenant = <from signed principal>` (never
from request body). Redis keys are tenant-prefixed; Kafka partitions are keyed by `tenant_id`;
identity clusters + bandit posteriors are tenant-scoped. **CI must include a cross-tenant negative test.**
Enterprise tenants may opt into a **db-per-tenant** isolation tier via connection routing (same code).

**Analytics-store isolation (ClickHouse — no Postgres-equivalent RLS):** compensating control =
ClickHouse **`CREATE ROW POLICY`** bound to a per-session tenant setting (from the principal) **plus** a
query layer that injects a mandatory `tenant_id` predicate server-side **plus** a CI test asserting no
analytics/dashboard query can execute un-scoped. Operator cross-tenant analytics = an explicit audited
RBAC capability, never an app default. Redis/Kafka isolation is by tenant-prefixing/partitioning (by
construction), not RLS.

## 3. Table groups (owned per `05` §1)
- **Subjects:** `companies`, `persons`, `records`, `identity_clusters` (+ identity graph edges) — idempotent,
  tenant-namespaced cluster IDs (ADR-0004).
- **Jobs:** `enrichment_jobs`, `job_items` (async unit; idempotent placement key).
- **Provider data:** `providers` (metadata), `provider_keys`, `key_pools`, `provider_results` (raw per-call).
- **Provenance & scoring (G5):** `field_versions` (append-only, **bitemporal** valid-time+system-time,
  retains winners **and** losers, W3C PROV lineage, pinned config-versions), `confidence_scores`.
- **Config-as-versioned-data:** `calibrators`, `provider_reliability_weights`, `reservation_values`,
  bandit priors/posteriors — referenced by version in every `field_versions` row for deterministic
  re-resolution (ADR-0005/0006/0007/0008).
- **Money & idempotency:** `idempotency_ledger` (unique constraint, G2), `cost_ledger` (reserve→
  commit→refund, G4), `credits`, `usage`, `billing`.
- **Ops:** `retry_history`, `provider_statistics`, `audit_logs`; `api_logs`/`system_logs`/`error_logs`
  in ClickHouse.

## 4. Provenance & versioning (G5)
`field_versions` is the golden-record history: each row = {value, calibrated confidence, log-odds,
provider, cost, fetched_at, valid/system time, is_winner, calibrator_version, weight_version, job_item_id,
prov_lineage}. Persistence path enforces a `NOT NULL` FK to the provenance/job_item so a **bare value
cannot be written** (structural G5). Re-resolution under new weights is deterministic because config
versions are pinned per row.

## 5. Partitioning & retention (bounds the ~1.38 TB/day raw est., `00` §4)
- High-volume tables (`provider_results`, `field_versions`, logs) partitioned by **time + tenant**.
- Retention **by data class**: raw provider payloads short TTL; provenance long-retained (audit);
  PII vs firmographic retention set per residency policy (`18`). Analytics offloaded to ClickHouse.
- Indexes for routing lookups: `provider_statistics(provider,field,region)`, identity keys, idempotency_key.

## 6. Open items
| ID | Item | Status |
|----|------|--------|
| DB-1 | Datastore + isolation model | ✅ ADR-0011 |
| DB-2 | `erd.mmd` | ✅ core ERD (extend with full DDL at impl) |
| DB-3 | Retention per data class (residency) | drafted; finalize with `18` |
| DB-4 | Multi-truth fields schema (WQ-4: email/phone can have several valid values) | `field_versions` supports N winners per field; policy in `08` |

## 7. Reviewer result (`/gate-check` Phase 6)
| Check | Result |
|-------|--------|
| G1 RLS at DB (not app-only) + negative test noted | PASS |
| G5 provenance structural (no bare writes; losers retained; config-versioned) | PASS |
| G2/G4 ledgers modeled (idempotency + cost) | PASS |
| Partitioning/retention bounds storage | PASS |
| ERD ↔ prose parity | PASS |
| Datastore decision has ADR + tradeoff | PASS (ADR-0011) |

**Gate:** `GATE-PASS` (auto-advance; recorded).
