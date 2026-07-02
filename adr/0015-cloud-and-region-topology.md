# ADR 0015 — Cloud & region topology: portability-first, AWS primary reference, regional cells

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Staff DevOps Engineer, Lead Enterprise Solutions Architect, Cost/Scale Reviewer
- **Phase:** 19 · **Source:** `docs/19-Deployment.md`

## Context
We must pick a cloud + region topology. The stack was chosen to be **portable** (Kafka-protocol via ADR-0013,
PostgreSQL/Redis/ClickHouse, Kubernetes, Temporal) — we deliberately rejected single-cloud queues (SQS/
Pub/Sub) in ADR-0013. Multi-tenancy + residency use **regional cells** (ADR-0010).

## Options considered
| Option | Pros | Cons |
|--------|------|------|
| **Portability-first, one primary cloud reference (chosen)** | avoids lock-in on the hot path; can go multi-cloud/relocate; single primary keeps ops focused | must avoid tempting proprietary managed services |
| Single-cloud, embrace proprietary managed (SQS/Pub/Sub/etc.) | lowest ops | lock-in; contradicts ADR-0013; harder residency across clouds |
| Full multi-cloud active-active from day 1 | max resilience | high complexity/cost; premature |

## Decision
**Portability-first** on **Kubernetes**, everything as **Terraform + Helm**. **Primary reference cloud =
AWS** (broadest managed coverage: EKS, Aurora Postgres, ElastiCache, MSK/Redpanda-Cloud, ClickHouse Cloud,
Temporal Cloud) — but **no proprietary lock-in on the hot path** (all core deps are portable), so Azure/GCP
are supported by swapping managed equivalents. **Regional cells** are the unit of deployment, scale,
blast-radius, and **data residency** ("stamp another cell" per region). **Release safety:** blue-green for
the stateless control-plane modulith; **canary** for execution-workers + new provider-adapters (progressive
rollout with automated rollback on SLO breach, `20`). **DR:** cross-region cell failover.

## Rationale
The architecture is already portable by design; committing to one proprietary cloud would waste that and
re-introduce the lock-in ADR-0013 avoided. A single primary reference (AWS) keeps ops focused while
portability keeps the exit open and enables per-region residency across clouds if a tenant requires it.

## Consequences
- Positive: no hot-path lock-in; residency via cells; standard IaC.
- Negative/accepted: we forgo some turnkey proprietary services (already decided in ADR-0013); portability
  costs some managed convenience.

## Verification
Terraform apply to a second cloud/region as a portability smoke test; canary + auto-rollback drill;
cross-region failover drill (`21`).
