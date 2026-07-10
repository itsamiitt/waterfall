# Waterfall Enrichment Engine — Documentation Root

> **API-first, multi-tenant B2B sales-intelligence enrichment platform.**
> No browser automation. No scraping. No manual workflows. Everything operates through provider APIs.

This repository is **plan-first**. No production code is written for any module until an
approved plan for that module exists (see [`skills/enrichment-discipline`](../skills/enrichment-discipline/SKILL.md)).

## How to read this repo

| Area | Path | Purpose |
|------|------|---------|
| Project framing | [`00-Project-Overview.md`](00-Project-Overview.md) | Vision, scope, **canonical glossary**, throughput target, success criteria |
| Tooling | [`00b-Tooling-And-Agents.md`](00b-Tooling-And-Agents.md) | The skills, agents, and slash-commands that enforce correctness |
| Research | `01-Market-Research.md` … `03-Provider-Research.md` | Verified, cited competitor + provider analysis |
| Architecture | `04` … `16` | System, data, queue, routing, security, cost design |
| Ops & product | `17` … `22` | Dashboard, security, deployment, monitoring, testing, roadmap |
| **Implementation** | `23` … `40` | Per-slice implementation records — the code that exists + how each was verified live |
| **Subsystem series** | [`waterfall-dashboard/`](waterfall-dashboard/00-overview.md) · [`research-intelligence/`](research-intelligence/00-overview.md) | Self-contained design series: the control-plane management dashboard, and the Research & Intelligence platform extension (ADRs 0025–0030) |
| **Front door** | [`../README.md`](../README.md) | Top-level README: what it is, the five gates, quickstart, `scripts/demo.sh` |
| Decisions | [`/adr`](../adr) | Architecture Decision Records (one file per decision) |
| Diagrams | [`/diagrams`](../diagrams) | Diffable Mermaid diagrams, kept in sync with prose |
| Trackers | [`IMPLEMENTATION_PROGRESS.md`](IMPLEMENTATION_PROGRESS.md), [`CHANGELOG.md`](CHANGELOG.md) | Living status |

## Status legend (used in every document header)

| Marker | Meaning |
|--------|---------|
| `DRAFT` | Being written; not yet reviewed |
| `IN-REVIEW` | Submitted to the Architecture Reviewer / Gap-Analysis agents |
| `APPROVED` | Passed its reviewer checklist at a gate |
| `BLOCKED` | Cannot progress; see "Open items" in the doc |

## Fact-verification legend (used on every factual claim about a provider/competitor)

| Marker | Meaning |
|--------|---------|
| `VERIFIED` | Has a `source_url` + `verified_date` from a real, citable source |
| `UNVERIFIED` | No citable source yet — the stated value is an **assumption**, not a fact |
| `FAIL` | A reviewer/auditor check failed; must be fixed or accepted as risk |
| `ACCEPTED-RISK` | Open item explicitly accepted by a human with written rationale + owner + date |

## The non-negotiable gate sequence

```
RESEARCH → BRAINSTORM → PLAN → APPROVAL → IMPLEMENT → VERIFY
```

Enforced by [`skills/enrichment-discipline`](../skills/enrichment-discipline/SKILL.md). Implementation
of any module is forbidden until that module has an `APPROVED` plan. Human approval is required at
each **GATE**: end of each planning phase, the Planning Completion Gate, anything destructive/
irreversible, and any secret/infrastructure change.

## Current phase

See [`IMPLEMENTATION_PROGRESS.md`](IMPLEMENTATION_PROGRESS.md). **As of 2026-07-01:** planning
phases 0–22 complete (22 docs, 16 ADRs, 10 diagrams); the Planning Completion Gate passed, and
**18 implementation slices** have landed and been verified — the engine, HTTP gateway, durable
transactional-outbox queue, and stdlib PostgreSQL persistence (RLS, SCRAM, TLS, migrations, DLQ +
redrive) all run and are tested live. Run the whole thing with [`../scripts/demo.sh`](../scripts/demo.sh).
Per-slice status and honest deferrals are in [`IMPLEMENTATION_PROGRESS.md`](IMPLEMENTATION_PROGRESS.md).
