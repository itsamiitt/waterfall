# 12 — Provider Key Management

**Status:** `IN-REVIEW` · **Owner:** Staff Security Engineer + Principal Backend Engineer · **Last updated:** 2026-07-01
**Gated by:** [api-integration](../skills/api-integration/SKILL.md) · [Security Auditor](../agents/security-auditor.md) · `/security-audit`

> Owns provider credentials at scale without ever exposing secrets. Keys are **leased to the
> egress-proxy** and injected there (ADR-0010) — they never enter worker/adapter memory.

## 1. Key pools
- **Unlimited keys per provider**, grouped into **key pools**; **weighted routing** across keys (raise a
  provider's effective rate/concurrency ceiling, `11`); **regional keys** for residency/regional endpoints.
- Per-(provider, key) **circuit breaker** (state in Redis) so one bad key doesn't kill a healthy provider.

## 2. Health, quotas & limits
- Per-key **health checks** + **quota/credit monitoring** (ingest provider quota headers — `x-ratelimit-*`,
  `x-call-credits-spent`; `01` K4); **daily + monthly limits**.
- **Automatic disable** on `AUTH`/`QUOTA` (incl. HTTP 402) errors; **automatic re-enable** on recovery
  probe; **automatic failover** to another key in the pool.
- Error monitoring maps provider quirks (e.g. Hunter 403 = throttle, not auth) per the `api-integration` taxonomy.

## 3. Routing & benchmarking
- **Priority routing** + weighted distribution across keys; least-loaded / most-quota-remaining first.
- Per-key **benchmarking**: cost, latency (measured), success-rate, error rate → feeds `provider_statistics`
  and the orchestrator's reservation values / Thompson posteriors (`08`).

## 4. Tenant isolation & cost attribution (G1/G4)
- Policy: **platform-shared** vs **per-tenant** keys (cost vs isolation/attribution tradeoff) — default
  shared for cost; per-tenant keys for enterprise tenants that bring their own or require attribution.
- Every call's cost is attributed to the tenant (cost_ledger, `16`); shared keys never leak cross-tenant data.

## 5. Provider supply-continuity (`01` §5 K8)
Track provider viability as a health signal — API sunset/deprecation (e.g. Clearbit standalone API sunset
2026) and supply takedowns (e.g. Seamless.AI LinkedIn takedown Mar 2025). Never sole-source; alert +
auto-failover on continuity loss.

## 6. Provider correlation / ownership graph (`03`, WQ-2)
For the copy-discount in confidence fusion (ADR-0005/0006) and to avoid false corroboration, record known
reseller/ownership links — e.g. NeverBounce ⊂ ZoomInfo, Datanyze ⊂ ZoomInfo, Ekata ⊂ Mastercard — and
treat providers sharing upstream data as correlated when weighting evidence.

## 7. Secrets (ties to `18`)
Keys stored in **secrets manager / Vault**; **never** logged, never in code, never at the API gateway.
Leased just-in-time to the egress-proxy; **rotation** procedure (scheduled + on-compromise) with overlap.

## 8. Open items
| ID | Item | Status |
|----|------|--------|
| KM-1 | Secrets backend | Vault/cloud secrets manager (finalize with `18`/`19`) |
| KM-2 | Quota/credit data model | ✅ (`06` `provider_keys`, `provider_statistics`) |
| KM-3 | Auto disable/enable/failover state machine | drafted (§2); detail at impl |

## 9. Reviewer result (`/security-audit` Phase 12)
| Check | Result |
|-------|--------|
| Keys never in code/logs/gateway; injected at egress only | PASS |
| Per-(provider,key) breaker + auto disable/enable/failover | PASS |
| Quota/credit monitoring ingests provider headers | PASS |
| Tenant cost attribution + no cross-tenant leak (G1) | PASS |
| Continuity + correlation signals tracked | PASS |
| Rotation procedure defined | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
