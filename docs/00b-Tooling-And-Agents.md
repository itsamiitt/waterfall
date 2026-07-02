# 00b — Tooling & Agents (Phase 0 Bootstrap)

**Status:** `APPROVED` (bootstrap) · **Owner:** Lead Enterprise Solutions Architect · **Last updated:** 2026-06-30

> Phase 0 builds the scaffolding that enforces correctness for the entire build **before** any
> research or design. This document is the index + contract for that tooling.

## 1. Why Phase 0 exists
A free-running build drifts and fabricates. We first build skills (encoded discipline), agents
(specialized reviewers), and commands (repeatable checklists), then run every later phase through
them. See [ADR-0003](../adr/0003-plan-first-gated-process.md).

## 2. Skills (`/skills/<name>/SKILL.md`)
| Skill | Enforces | Used in |
|-------|----------|---------|
| [enrichment-discipline](../skills/enrichment-discipline/SKILL.md) | The gate sequence RESEARCH→BRAINSTORM→PLAN→APPROVAL→IMPLEMENT→VERIFY; no code before approved plan | every phase + module |
| [provider-research](../skills/provider-research/SKILL.md) | Uniform Provider template; cite or `UNVERIFIED` | Phases 1, 3; `/provider-audit` |
| [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) | G1 tenant isolation, G2 idempotency, G3 bounded calls, G4 cost ceiling, G5 provenance, + SSRF | every enrichment module |
| [api-integration](../skills/api-integration/SKILL.md) | Uniform Provider adapter: auth, retry+jitter, breaker, timeout, rate-limit, error taxonomy, idempotency, logging | every Provider adapter |
| [doc-consistency](../skills/doc-consistency/SKILL.md) | Glossary single-name rule; diagram↔prose↔schema parity; diffable diagrams | every doc + `/architecture-review` |

## 3. Agents (`/agents/<name>.md`)
Each has explicit inputs, outputs, and a checklist (never prose-only verdicts).
| Agent | Mandate | Invoked at |
|-------|---------|-----------|
| [Research Agent](../agents/research-agent.md) | Gather + verify Provider/competitor data; no `VERIFIED` without a citable source | research phases |
| [Architecture Reviewer](../agents/architecture-reviewer.md) | Audit docs vs hard gates → pass/fail checklist | every gate |
| [Gap-Analysis Agent](../agents/gap-analysis-agent.md) | Planned scope vs delivered artifacts; concrete missing items | every gate + Planning Completion |
| [Security Auditor](../agents/security-auditor.md) | Tenant isolation, IDOR, SSRF, secrets, residency | every data-path doc/module |
| [Implementation Agent](../agents/implementation-agent.md) | Code strictly from approved plans; refuse undocumented work | implementation |
| [Cost/Scale Reviewer](../agents/cost-scale-reviewer.md) | Throughput/cost math, queue/back-pressure sizing | capacity/cost docs |

## 4. Commands (`/commands/<name>.md`)
Each runs an agent's checklist and writes results back into the repo.
| Command | Runs | Writes to |
|---------|------|-----------|
| [/provider-audit](../commands/provider-audit.md) | Research Agent | `01`/`03` + matrix |
| [/architecture-review](../commands/architecture-review.md) | Architecture Reviewer | target doc + Open items |
| [/security-audit](../commands/security-audit.md) | Security Auditor | `18` + module security section |
| [/scale-check](../commands/scale-check.md) | Cost/Scale Reviewer | `11`/`16` + Open items |
| [/gap-analysis](../commands/gap-analysis.md) | Gap-Analysis Agent | `IMPLEMENTATION_PROGRESS.md` |
| [/gate-check](../commands/gate-check.md) | all of the above | `IMPLEMENTATION_PROGRESS.md` + chat gate output |

## 5. How they compose at a GATE
```
/gate-check <phase>
  ├─ /gap-analysis      → missing items
  ├─ /architecture-review → G1–G5 + SSRF + diagram/glossary parity
  ├─ /security-audit    → tenant isolation + SSRF + secrets + residency  (if data paths touched)
  └─ /scale-check       → throughput + cost math                          (if capacity/cost touched)
  ⇒ emit enrichment-discipline GATE checklist → STOP for human approval
```

## 6. Execution mapping (how this runs in the actual harness)
The skills/agents/commands above are the **canonical specs** (diffable, in-repo, the source of
truth). In this Claude Code harness they map to runtime mechanisms as follows:
- **Skills** → consulted as authoritative checklists; their rules gate every edit.
- **Agents** → realized as subagent runs (the Agent/Workflow tools) prompted with the agent's spec +
  checklist; research/review phases fan out via the Workflow tool for parallel, adversarially
  verified coverage.
- **Commands** → repeatable procedures a maintainer runs by name; each writes results back into the
  repo as specified.
> Note: these specs intentionally live under `/skills`, `/agents`, `/commands` per the required repo
> structure. A future enhancement may mirror them into `.claude/` to make them directly invocable as
> native Claude Code skills/commands; that mirror, if added, must stay generated-from / in-sync-with
> these canonical files (a `doc-consistency` rule) to avoid a second source of truth.

## 7. Open items
| ID | Item | Status |
|----|------|--------|
| T-1 | Optional `.claude/` mirror of skills+commands for native invocation | Deferred (enhancement) |
| T-2 | Wire `/gate-check` sub-commands to emit machine-readable JSON results | Deferred to Phase 21 (testing tooling) |
