# Command: /gap-analysis

**Runs:** [Gap-Analysis Agent](../agents/gap-analysis-agent.md)

## Purpose
Diff planned scope vs. delivered artifacts; list concrete, located missing items; compute honest
coverage.

## Usage
`/gap-analysis [phase number | "all"]`

## Steps
1. Build the requirement inventory from the prompt scope + `00` + `README` doc map + trackers.
2. For each requirement, locate the delivering artifact; classify `yes`/`partial`/`no`.
3. Write an actionable, located action for every `partial`/`no`.
4. Recompute coverage % per phase + overall (never 100% with any `partial`/`no`).
5. Write the gap table into `IMPLEMENTATION_PROGRESS.md`; update `CHANGELOG.md`.

## Output (written to repo)
- Gap table: requirement → expected artifact → found? → location → action.
- Coverage % per phase + overall.

## Pass criteria
- No reframed-as-complete requirements; every gap actionable + located; coverage math honest.
