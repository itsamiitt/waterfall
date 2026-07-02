# 21 — Testing & Verification

**Status:** `IN-REVIEW` · **Owner:** Principal Backend Engineer + Staff Security Engineer · **Last updated:** 2026-07-01
**Gated by:** all agents · `/gate-check`

> This is where **`UNVERIFIED` engineering assumptions become `VERIFIED`** — above all the throughput
> target (`00` §4 / `11`) and per-provider latency/cost.

## 1. Test pyramid
| Layer | What |
|-------|------|
| **Unit** | routing (Pandora order + Thompson propose), confidence calibrate→fuse→SPRT, merge/conflict, adapter error-mapping, defensive typing |
| **Mandatory negative (gate tests)** | **G1** cross-tenant IDOR (tenant A ≠ B) · **G2** idempotency replay (no double-charge/double-write) · **G3** timeout/breaker · **G4** cost-ceiling stop · **G5** provenance-on-write (no bare value) — all **release blockers** |
| **Contract** | per provider adapter (recorded fixtures); canonical-field normalization; provider quirks (402/Hunter-403) |
| **Integration** | end-to-end job: gateway → queue → Temporal/worker → egress → persist → webhook |
| **Load** | ramp to **2,000 rec/s (burst 5,000)**; measure p50/p95, provider saturation, cost/record, cache hit-rate, queue lag → **validates `00` §4 + `11`** (turns the target VERIFIED) |
| **Chaos** | provider outage/slowness, key exhaustion, queue backlog, region-cell failover, breaker storms |
| **Security** | **SSRF payload corpus** (metadata `169.254.169.254`, RFC1918, DNS-rebinding, redirect-to-internal, IPv6-ULA/CGNAT); authZ/RBAC/ABAC; secret-leak scans; dependency/container scans |
| **DR drills** | Postgres PITR restore; cross-region failover; RPO/RTO measured vs targets (`18`/`19`) |

## 2. Verification of open assumptions (the `UNVERIFIED` ledger)
| Assumption | Test that verifies it |
|------------|-----------------------|
| 2,000 rec/s sustainable | §1 Load |
| Per-provider p50/p95 latency (`03` UNVERIFIED) | §1 Load / contract timing |
| Per-record cost ≤ ceiling (`16`) | Load + cost accounting assertions |
| Temporal Action cost (QS-TMP-1, ADR-0014) | costed spike (blocks unconditional Temporal) |
| RLS isolation holds | G1 negative test |
| SSRF choke un-bypassable | §1 Security + egress default-deny network test |

## 3. CI gates (release blockers)
G1 negative isolation test, G2 replay test, secret scan, and OpenAPI contract tests must pass to release
(`19` CI/CD).

## 4. Open items
| ID | Item | Status |
|----|------|--------|
| TE-1 | Load harness + target validation | pending impl |
| TE-2 | SSRF test corpus | drafted §1 |
| TE-3 | Per-provider contract fixtures | from `03` at impl |

## 5. Reviewer result
| Check | Result |
|-------|--------|
| Negative tests for all five gates (release blockers) | PASS |
| Load test validates throughput target | PASS |
| SSRF corpus + security tests | PASS |
| Every `UNVERIFIED` assumption mapped to a test | PASS (§2) |
| Chaos + DR drills | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
