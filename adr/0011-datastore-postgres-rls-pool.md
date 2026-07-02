# ADR 0011 — Datastore & tenant-isolation: PostgreSQL RLS-pool (+ Redis + ClickHouse)

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Database Architect, Staff Security Engineer, Cost/Scale Reviewer
- **Phase:** 6 · **Source:** `docs/06-Database-Architecture.md` (ratifies ADR-0010's provisional pick)

## Context
We need an OLTP system-of-record that enforces tenant isolation (G1) at the database, stores provenance
(G5) and ledgers (G2/G4), scales to ~2,000 rec/s writes across many tenants, and supports offline
analytics (~1.38 TB/day raw). Isolation model options carry very different ops/scale/isolation tradeoffs.

## Options considered — tenant isolation model
| Model | Isolation | Ops (patch/backup/migrate) | Connection/catalog cost | Best for |
|-------|-----------|----------------------------|-------------------------|----------|
| **RLS pool (shared tables, tenant_id + FORCE RLS)** | strong (DB-enforced) | **1 cluster** | **low** | the default at many-tenant scale |
| Schema-per-tenant | stronger | migration × N schemas | high catalog bloat | dozens–hundreds of tenants |
| DB-per-tenant | strongest (physical) | N clusters to operate | highest | a few enterprise tenants |

## Options considered — engines
- OLTP: **PostgreSQL** (mature RLS, bitemporal-friendly, ubiquitous managed offerings) vs MySQL (weaker
  RLS story) vs a distributed SQL (CockroachDB/Yugabyte — more ops, needed only past single-primary limits).
- Analytics: **ClickHouse** (columnar, cheap high-ingest) vs BigQuery/Snowflake (managed, pricier per-scan).
- Cache/coordination: **Redis** (atomic ops for reservations/breakers/tokens).

## Decision
**PostgreSQL** as OLTP system-of-record with **RLS-pool** multi-tenancy: every tenant-scoped table has
`tenant_id` + `FORCE ROW LEVEL SECURITY`; the hot-path role has **no `BYPASSRLS`**; `SET LOCAL
app.current_tenant` is set per transaction from the signed principal context (ADR-0010). A **db-per-tenant
isolation tier** is offered to enterprise tenants that contractually require physical isolation, **reusing
the same code** via connection routing. **Redis** for cache/preview, idempotency fast-path, atomic
credit/cost reservations, breaker state, per-provider concurrency tokens. **ClickHouse** for analytics
(api_logs, usage, provider_statistics, history) via CDC. **Config-as-versioned-data** (calibrators,
reservation_values, provider_reliability_weights, bandit priors) lives in Postgres, referenced by version
in every provenance row. Provenance (`field_versions`) is **append-only / bitemporal** (valid-time +
system-time), retaining losing candidate values.

## Rationale
RLS-pool gives DB-enforced isolation at the lowest ops + connection cost, which is what many-tenant
2,000 rec/s demands; schema/db-per-tenant explode catalog/connection overhead, so they are reserved for
the enterprise tier where physical isolation is contractually required. Postgres+Redis+ClickHouse is a
portable, well-understood stack available managed on all major clouds (keeps `19` cloud choice open).

## Consequences
- Positive: single cluster to operate for the pool; G1 enforced by the DB; provenance + ledgers are
  first-class transactional data (G2/G4/G5 in the same txn as the value).
- Negative/accepted: RLS discipline is critical (a stray `BYPASSRLS` leaks tenants) → CI
  **negative-isolation test** is mandatory; single-primary write ceiling → shard by tenant / add a
  distributed-SQL tier only if load tests (`21`) exceed it (re-open this ADR then).

## Verification
CI cross-tenant negative test (tenant A cannot read B); write-throughput load test to 2,000 rec/s (`21`);
provenance-completeness test (losers retained, config-versions pinned).
