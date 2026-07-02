---
name: enrichment-discipline
description: >-
  Enforces the non-negotiable gate sequence RESEARCH → BRAINSTORM → PLAN → APPROVAL → IMPLEMENT →
  VERIFY for the Waterfall Enrichment Engine. No code is written for any module before an APPROVED
  plan exists for that module. Invoke at the start of every module and at every GATE.
version: 1.0.0
---

# Skill: enrichment-discipline

## Purpose
Prevent drift, hallucination, and premature implementation by forcing a fixed sequence of gates.
This is the master process skill; the other four skills plug into its phases.

## The sequence (per module AND per phase)
```
RESEARCH → BRAINSTORM → PLAN → APPROVAL(GATE) → IMPLEMENT → VERIFY(GATE)
```

| Step | Done means | Produced artifact | Skill used |
|------|------------|-------------------|------------|
| RESEARCH | Facts gathered with `source_url` + `verified_date`; unknowns flagged `UNVERIFIED` | research doc section | `provider-research` |
| BRAINSTORM | ≥2 options compared with tradeoffs surfaced | options table + recommendation | — |
| PLAN | A concrete, testable plan + diagrams + schema deltas | plan doc + Mermaid diagram | `doc-consistency` |
| APPROVAL | Human approves at the GATE; reviewer checklist passes | gate checklist result | run `/architecture-review`, `/gap-analysis` |
| IMPLEMENT | Code matches the APPROVED plan exactly | code + tests | `api-integration`, `waterfall-correctness` |
| VERIFY | Tests + security + scale checks pass | passing checks, updated trackers | `/security-audit`, `/scale-check` |

## Hard rules
1. **No code before APPROVED plan.** If a module has no `APPROVED` plan, refuse to write its code.
2. **No skipping steps.** BRAINSTORM with one option = fail; produce a real alternative.
3. **No silent reframing.** If a requirement looks already-met, prove it with an artifact link, or
   flag the gap. Reframing a requirement to look complete is itself a `FAIL`.
4. **Stop at GATES.** End of each planning phase, the Planning Completion Gate, anything
   destructive/irreversible, and any secret/infra change require explicit human approval.
5. **Discovery during IMPLEMENT → update PLAN first.** If implementation reveals a needed change,
   update the plan + diagrams + schema, re-run the reviewer, then write code.
6. **Every factual claim carries provenance** or is `UNVERIFIED`. No confident-but-uncited numbers.

## GATE checklist (emit verbatim at each gate)
- [ ] All RESEARCH claims have `source_url` + `verified_date`, or are flagged `UNVERIFIED`.
- [ ] BRAINSTORM compared ≥2 options with tradeoffs + a recorded decision (ADR if architectural).
- [ ] PLAN is testable and its diagrams/schema match the prose (`doc-consistency` passed).
- [ ] Architecture Reviewer checklist: PASS (no prose-only approvals).
- [ ] Gap-Analysis: no missing planned items.
- [ ] Security Auditor: tenant-isolation + SSRF + secrets checks PASS for anything touching those.
- [ ] No unaccepted `FAIL`/`UNVERIFIED` items remain (each open item is `ACCEPTED-RISK` w/ owner+date).
- [ ] Trackers updated: `IMPLEMENTATION_PROGRESS.md` + `CHANGELOG.md`.

## Output discipline
At a gate emit only: (1) the checklist result, (2) the list of open items, (3) a short approval
request. Between gates, work in repo files, not chat prose.
