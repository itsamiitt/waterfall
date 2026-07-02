---
name: doc-consistency
description: >-
  Rules ensuring every diagram, schema, and API contract stays cross-referenced and in sync, and
  that the canonical glossary is used everywhere (the same entity is never named two different
  ways). Invoke before marking any document done and during /architecture-review.
version: 1.0.0
---

# Skill: doc-consistency

## Purpose
Keep prose, diagrams, schema, and API contracts mutually consistent and use one vocabulary. A
document is **not done** until its diagrams match its prose and its schema.

## Single sources of truth
| Concern | Canonical location |
|---------|--------------------|
| Entity/term names (Glossary) | `docs/00-Project-Overview.md` §7 |
| Field vocabulary | `docs/00-Project-Overview.md` §7 (Field list) |
| Status + verification legends | `docs/README.md` |
| Data model (ERD) | `docs/06-Database-Architecture.md` + `diagrams/erd.mmd` |
| System architecture | `docs/04-System-Architecture.md` + `diagrams/architecture.mmd` |
| Service catalog | `docs/05-Microservices.md` + `diagrams/dependencies.mmd` |
| Enrichment methodology pipeline | `docs/02-Waterfall-Research.md` + `diagrams/enrichment-pipeline.mmd` |
| Queue/event/retry flow | `docs/10-Queue-System.md` + `diagrams/{queue-flow,event-flow,retry-flow}.mmd` |
| Deployment/infra | `docs/19-Deployment.md` + `diagrams/{deployment,infrastructure}.mmd` |
| API contracts | `docs/07-API-Gateway.md` |
| Decisions | `/adr/*.md` (index `adr/README.md`) |

## Rules
1. **One name per entity.** Use Glossary terms exactly. If a new term is needed, add it to the
   Glossary first, then use it. Never introduce a synonym (e.g. "lead" for Person).
2. **Diagram ↔ prose ↔ schema parity.** Any entity/relationship/flow in prose must appear in the
   relevant diagram and (if persisted) the ERD/schema, and vice-versa. No orphan boxes.
3. **Diffable diagrams only.** Mermaid (or equivalent text) in `/diagrams`, referenced from docs.
   No binary images as the source of truth.
4. **Cross-references resolve.** Every `see XX` / `[[link]]` points to a real doc/section.
5. **Every doc has a header**: Status + Owner + Last updated, and an "Open items" table at the end.
6. **Every factual claim about a provider/competitor** uses `provider-research` rules (cited or
   `UNVERIFIED`).
7. **Change propagation.** If a change in one doc affects another (schema rename, new service, new
   field), update all affected docs/diagrams **in the same change** and log it in `CHANGELOG.md`.

## Consistency checklist (run before marking a doc done; part of /architecture-review)
- [ ] All terms match the Glossary; no synonyms introduced.
- [ ] Every entity/flow in prose appears in a `/diagrams/*.mmd` file and (if persisted) the ERD.
- [ ] Every diagram element appears in prose/schema (no orphans).
- [ ] All cross-references + links resolve.
- [ ] Header (Status/Owner/Updated) + "Open items" table present.
- [ ] Provider/competitor facts cited or `UNVERIFIED`.
- [ ] `CHANGELOG.md` updated for any propagated change.

## Glossary-drift quick test
Grep the repo for known forbidden synonyms (`lead`, `vendor API`, `cascade`, `account` used for
Company, etc.); each hit is a `FAIL` to fix or justify.
