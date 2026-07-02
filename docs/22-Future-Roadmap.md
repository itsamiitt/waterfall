# 22 — Future Roadmap

**Status:** `IN-REVIEW` · **Owner:** Senior Product Manager · **Last updated:** 2026-07-01
**Gated by:** [Architecture Reviewer](../agents/architecture-reviewer.md) · `/architecture-review`

> Deliberately-deferred scope, tracked so it isn't lost and the core stays focused.

## 1. Backlog (prioritized after Planning Completion Gate)
| Item | Rationale / origin |
|------|--------------------|
| **Temporal cost-spike + decision** (QS-TMP-1) | resolve ADR-0014 gate; else hand-rolled saga |
| **Self-serve provider onboarding** (tenant BYO keys) | key-manager already supports per-tenant keys (`12`) |
| **ML-router maturity** — graduate segmented Thompson → LinUCB/Gittins per field | ADR-0008; needs measured data |
| **GraphQL external API** | deferred in ADR-0012 if tenants need flexible field selection |
| **Deepen weak-verification providers** (Melissa/Ekata/Diffbot/WhoisXML) | `03` PR-IDV-1 |
| **Full provider correlation/copy graph** | WQ-2; improves copy-discount (ADR-0006) |
| **Real-time streaming enrichment SLAs** (webhook-in→enrich→webhook-out) | product |
| **Additional regions + residency certifications** | ADR-0015 cells |
| **Privacy automation** — per-person suppression, DSAR automation | GDPR/CCPA (`18`) |
| **Provider cost-bidding / dynamic price optimization** | `16` |
| **`.claude/` mirror of skills/commands** (native invocation) | T-1 (`00b`) |
| **Multi-play/parallel-stop bandit extension** | WQ-9 |

## 2. Explicitly out of scope (unchanged)
Browser automation, scraping, manual workflows (ADR-0002) remain permanently out of scope.

## 3. Open items
| ID | Item | Status |
|----|------|--------|
| RM-1 | Prioritize backlog post-gate | pending Planning Completion Gate |

## 4. Reviewer result
| Check | Result |
|-------|--------|
| Deferred items captured with origin | PASS |
| Out-of-scope constraints reaffirmed | PASS |
| Links to originating open items resolve | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
