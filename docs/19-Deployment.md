# 19 — Deployment

**Status:** `IN-REVIEW` · **Owner:** Staff DevOps Engineer · **Last updated:** 2026-07-01
**Gated by:** [Cost/Scale Reviewer](../agents/cost-scale-reviewer.md) · [Security Auditor](../agents/security-auditor.md) · `/architecture-review` + `/security-audit`

> Cloud + topology: **[ADR-0015](../adr/0015-cloud-and-region-topology.md)** (portability-first, AWS primary
> reference, regional cells). Diagrams: [`deployment.mmd`](../diagrams/deployment.mmd), [`infrastructure.mmd`](../diagrams/infrastructure.mmd).

## 1. Packaging & IaC
- **Docker** images per deployable (control-plane modulith, workers, egress-proxy, Temporal workers, dashboard).
- **Kubernetes** (EKS primary; AKS/GKE supported) — everything as **Terraform** (infra) + **Helm** (workloads).
- **Config-as-data** (calibrators/weights/reservation values) shipped as data, not releases (`06`).

## 2. Network zones (SSRF-critical, `13`/`18`)
| Zone | Contents | Internet egress |
|------|----------|-----------------|
| Public | WAF + edge LB | inbound only |
| App (private) | control-plane modulith, workers | **none (default-deny)** |
| Egress | **egress-proxy fleet** | **the only zone with an outbound route** |
| Data (private) | Postgres, Redis, Kafka/Redpanda, ClickHouse, Vault | none |

## 3. Topology — regional cells
Each **cell** = a full stack in one region (residency + blast-radius + scale unit). Scale out by "stamping
another cell". Cross-region **async replication** for DR + failover. Cross-cell traffic minimized (residency).

## 4. CI/CD
GitHub Actions: **build → test (unit+negative G1–G5) → scan (SAST/deps/secrets/container) → Terraform plan
→ Helm deploy**. **Blue-green** for the stateless control-plane modulith; **canary** for execution-workers +
new provider-adapters (progressive %, automated rollback on SLO breach — `20`). Migrations gated + reversible;
RLS negative-isolation test is a **release blocker**.

## 5. Scaling & LB
Horizontal scaling via HPA (control-plane on p95/CPU; workers on **queue lag**; egress-proxy on rps) with
finite caps (`11`); L7 load balancers; GeoDNS to the nearest residency-appropriate cell.

## 6. Disaster recovery
Cross-region cell failover; Postgres PITR; object-store for tiered Kafka/ClickHouse; **RPO ≤ 5 min / RTO ≤
1 hr** targets validated by restore + failover drills (`21`).

## 7. Open items
| ID | Item | Status |
|----|------|--------|
| DP-1 | Primary cloud | ✅ ADR-0015 (AWS reference, portable) |
| DP-2 | Region topology + DR region | ✅ §3/§6 (finalize regions with residency needs) |
| DP-3 | `deployment.mmd` + `infrastructure.mmd` | ✅ Phase 19 |

## 8. Reviewer result (`/gate-check` Phase 19)
| Check | Result |
|-------|--------|
| Cloud/topology has ADR + tradeoff | PASS (ADR-0015) |
| Default-deny egress except proxy (SSRF) | PASS (§2) |
| Regional cells for residency + blast radius | PASS |
| CI/CD with scans + blue-green/canary + auto-rollback | PASS |
| RLS negative test is a release blocker | PASS |
| DR RPO/RTO + drills | PASS |
| Diagrams ↔ prose | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
