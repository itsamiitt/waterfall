# ADR 0001 — Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-06-30
- **Deciders:** Lead Enterprise Solutions Architect
- **Phase:** 0

## Context
This is a large, multi-phase, multi-role build where role conflicts (latency vs correctness, cost vs
coverage, speed vs compliance) recur. Decisions must be traceable, revisable, and reviewable in a
diffable way, and the process explicitly requires conflicts to be surfaced and their rationale
recorded.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. Lightweight Nygard-style ADRs in `/adr`, append-only | Diffable, low overhead, industry-standard | Discipline needed to keep current | Simplicity vs richness |
| B. Decisions embedded only in design docs | Co-located with context | Hard to find, gets edited/lost, no clear status | Locality vs traceability |
| C. External wiki/tool | Rich | Not diffable in-repo, extra tool, drifts from code | Features vs in-repo truth |

## Decision
Use lightweight, append-only Nygard-style ADRs in `/adr`, one file per decision, with the
[template](0000-adr-template.md). Superseding decisions get new ADRs.

## Rationale
ADRs are diffable, live next to the code/docs, and force the "surface the conflict + rationale"
discipline the process requires. Chose in-repo simplicity over external tooling for traceability.

## Consequences
- Positive: every conflict decision is reviewable and has a status; back-propagation is auditable.
- Negative: requires updating the ADR index on each new ADR.
- Follow-ups: queue/datastore/cloud/orchestration ADRs will be added in their phases.

## Verification
The Architecture Reviewer checklist requires "each non-trivial tradeoff has an ADR" — enforced at
every gate.
