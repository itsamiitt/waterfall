# 16 — Cost Optimization

**Status:** `IN-REVIEW` · **Owner:** Cost/Scale Reviewer + Senior Product Manager · **Last updated:** 2026-07-01
**Gated by:** [Cost/Scale Reviewer](../agents/cost-scale-reviewer.md) · `/scale-check`

> The economic core of the waterfall: maximize coverage/confidence per dollar under hard ceilings.

## 1. Cost ceilings (G4)
Per-record, per-job, per-tenant ceilings; **pre-flight enforcement** (sum committed provider costs,
truncate the Pandora tail so committed ≤ ceiling) + **atomic reservation** (Redis-backed, Postgres-durable)
+ **reconciliation** (refund-on-miss where the provider doesn't bill misses; provider-specific from `03`).
No provider call leaves without a confirmed reservation (`04` §4, `09`).

## 2. Charge-on-success metering (`01` §5 K2)
The field-wide model is **"pay for what you find"** — meter cost on provider **success**, not request
count. **Split cost buckets** (Clay's model): **Data Credits** (provider on-hit cost) vs **orchestration
compute** (per-run) so deep waterfalls don't erase margin.

## 3. Per-record cost math
Expected cost = Σ over ordered providers of `P(reach step) × price_on_hit`; the Pandora cheap-first cascade
+ **SPRT early-stop** keep the paid mean near ~3.2 calls; **worst-case ≤ ceiling** by pre-flight truncation.
> Absolute per-record cost is `UNVERIFIED` until provider prices (`03`) feed a spreadsheet; several key
> prices are cited (Prospeo ~$0.01/email, Telnyx ~$0.0015–0.003/lookup, etc.).

## 4. Caching (protect margin + honor terms)
- **Result cache** (TTL by data class: firmographics long, contact PII shorter, intent ~7-day) + **negative
  cache** (known-miss, avoid re-paying for a miss).
- **Cache-before-reveal:** cheap discovery/preview + dedup before any paid reveal (`08`).
- **Cross-tenant sharing only for non-PII firmographics** if policy explicitly allows (default off, G1).
- Providers contractually expect caching of purchased records (`01` K7) — align.

## 5. Order optimization (ties to `08`)
Cheapest-viable-first vs highest-confidence-first is resolved by the **reservation-value index** (ADR-0007):
value-per-dollar ordering (Thompson `reward − λ·cost`, ADR-0008) avoids over-paying; bounded parallel prefix
trades a little spend for latency only under the ceiling.

## 6. Credit accounting & analytics
- Real-time credit/quota tracking per provider key (`12`); alerts on exhaustion; auto-failover.
- **Cost analytics** per tenant / provider / field (ClickHouse, `06`) → dashboard (`17`); KPIs: cost-per-match,
  incremental lift per provider, cache hit-rate (`20`).

## 7. Open items
| ID | Item | Status |
|----|------|--------|
| CO-1 | Per-record cost model | `UNVERIFIED` — build sheet from `03` prices |
| CO-2 | Cache TTLs per data class (residency-aware) | drafted; finalize with `18` |
| CO-3 | Charge-on-miss handling per provider | from `03`; per-adapter config |

## 8. Reviewer result (`/scale-check` Phase 16)
| Check | Result |
|-------|--------|
| Ceilings pre-flight enforced + reserved + reconciled (G4) | PASS |
| Charge-on-success + Data-Credits/compute split | PASS |
| Cache-before-reveal + negative cache; cross-tenant only non-PII | PASS |
| Retries can't breach ceiling (`11` §5) | PASS |
| Cost numbers depending on uncited prices = `UNVERIFIED` | PASS (honest) |

**Gate:** `GATE-PASS` (auto-advance; recorded — CO-1 `ACCEPTED-RISK`).
