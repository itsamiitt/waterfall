# Command: /architecture-review

**Runs:** [Architecture Reviewer](../agents/architecture-reviewer.md) · **Skills:** [waterfall-correctness](../skills/waterfall-correctness/SKILL.md), [doc-consistency](../skills/doc-consistency/SKILL.md)

## Purpose
Audit a design document (and its diagrams + schema) against the hard gates and produce a
**pass/fail checklist**, not prose approval.

## Usage
`/architecture-review <doc path | phase number>`

## Steps
1. Load the target doc + referenced `/diagrams/*.mmd` + schema deltas + the Glossary.
2. Run the Architecture Reviewer checklist item by item (G1–G5, SSRF, diagram parity, glossary,
   failure modes, ADRs, citations, cross-refs).
3. For each `FAIL`, write a located, actionable required-change.
4. Write the checklist result into the doc's "Reviewer result" block + "Open items" table.
5. Update `CHANGELOG.md`. Recommend `GATE-PASS` only if zero unaccepted `FAIL`/`UNVERIFIED`.

## Output (written to repo)
- A `PASS`/`FAIL`/`N-A` checklist appended to the doc.
- A required-changes list (file:section + action) for every `FAIL`.
- A gate recommendation.

## Pass criteria
- Every checklist item `PASS` or `N-A`; G1–G5 + SSRF evidenced (not asserted); diagrams match prose.
