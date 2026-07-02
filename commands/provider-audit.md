# Command: /provider-audit

**Runs:** [Research Agent](../agents/research-agent.md) · **Skill:** [provider-research](../skills/provider-research/SKILL.md)

## Purpose
Add or refresh Provider/competitor entries with full citation discipline, then write results into
`03-Provider-Research.md` (providers) or `01-Market-Research.md` (competitors).

## Usage
`/provider-audit <provider-or-competitor name | category>`

## Steps
1. Resolve target(s); load the `provider-research` uniform template + fixed units.
2. Research primary sources (API docs, pricing, trust/compliance, status page).
3. Fill every template cell; cite (`source_url` + `verified_date`) or mark `UNVERIFIED`.
4. Add a waterfall-placement hypothesis.
5. Run the Research Agent checklist; fix gaps.
6. Append the entry + its sources into the target doc; update the comparison matrix row.
7. Update `CHANGELOG.md`.

## Output (written to repo)
- A completed entry in `01`/`03` + matrix row.
- Updated citation list.
- A short "could not verify" note for any `UNVERIFIED` cells.

## Pass criteria
- Zero un-cited factual cells (each cited or `UNVERIFIED`); units normalized; API-first respected.
