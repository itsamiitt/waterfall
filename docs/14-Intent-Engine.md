# 14 — Intent Engine

**Status:** `IN-REVIEW` · **Owner:** GTM Data Platform Architect · **Last updated:** 2026-07-01
**Gated by:** [provider-research](../skills/provider-research/SKILL.md) · [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) · `/architecture-review`

> Intent/signals are a **separate lane** from contact enrichment (different data model + update cadence),
> keyed on `company_domain`/account, run **async/batch**. Providers confirmed in [`03`](03-Provider-Research.md).

## 1. Sources (cited in `03`)
- **Intent topics:** Bombora, 6sense, Demandbase, G2 Buyer Intent, HG Insights.
- **Signals:** PredictLeads (job postings, technology changes, company/news), People Data Labs, Coresignal(D).
- Intent is **non-substitutable** (Bombora claims 86% exclusive) → ordered independently, not a fallback tier.

## 2. Model
- **Topic taxonomy** normalized across providers to a canonical topic set; per-tenant topic relevance.
- **Scoring + decay:** surge score with time decay; freshness half-life per signal type (WQ-5); dedup +
  cache before calling (flat/annual pricing → cache aggressively; ~7-day Surge TTL for Bombora, `01`).
- Confidence/provenance (G5) on every stored signal, same as fields.

## 3. Ingestion & delivery
- **Batch/async lane:** create-report → poll, or scheduled CSV/S3 drops; **webhook push** where offered
  (surging accounts) preferred over polling.
- Delivered to tenants via query API + webhooks (`07`), keyed on company/domain (account).
- **All five gates apply to signal-provider calls too:** G1 tenant isolation, G2 idempotency, **G3
  bounded (timeout/breaker) — signal providers are polled/fetched via the SSRF-safe egress-proxy (`13`),
  never fetched directly** (S3/report pulls + polling are outbound calls), G4 cost ceiling, G5 provenance.

## 4. Storage
`intent_data` (time-series; time+tenant partitioned; retention by policy, `06`); provenance preserved.

## 5. Open items
| ID | Item | Status |
|----|------|--------|
| IN-1 | Intent/signal providers | ✅ confirmed (`03`) |
| IN-2 | Scoring + decay model | drafted; tune with data |
| IN-3 | Signal webhook schema | align with `07` at impl |

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Providers cited (`03`) | PASS |
| Separate lane / async cadence | PASS |
| G1–G5 apply to signals (incl. **G3 bounded + egress-proxy** for polling/S3 pulls) | PASS |
| Cache/decay policy (cost) | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
