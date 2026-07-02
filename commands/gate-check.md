# Command: /gate-check

**Runs:** all reviewer agents in sequence · **Skill:** [enrichment-discipline](../skills/enrichment-discipline/SKILL.md)

## Purpose
The composite gate. Aggregates `/architecture-review`, `/security-audit`, `/scale-check`, and
`/gap-analysis` into one gate decision and emits the canonical GATE checklist for human approval.

## Usage
`/gate-check <phase number | "planning-completion">`

## Steps
1. Run `/gap-analysis`, `/architecture-review`, `/security-audit` (if data paths touched),
   `/scale-check` (if capacity/cost touched) on the phase's docs.
2. Collect every `FAIL`/`UNVERIFIED`; for each, require a fix or an `ACCEPTED-RISK` (owner + date +
   rationale).
3. Emit the `enrichment-discipline` GATE checklist verbatim with PASS/FAIL per line.
4. Write the gate result + open-items list into `IMPLEMENTATION_PROGRESS.md`; update `CHANGELOG.md`.
5. Output ONLY: (1) checklist result, (2) open items, (3) approval request. **Stop for human approval.**

## Output (written to repo + chat)
- Repo: gate result block in `IMPLEMENTATION_PROGRESS.md`.
- Chat: the 3-part gate output (checklist, open items, approval request) — nothing else.

## Pass criteria
- All sub-checks PASS; zero unaccepted `FAIL`/`UNVERIFIED`; human approval recorded before advancing.
