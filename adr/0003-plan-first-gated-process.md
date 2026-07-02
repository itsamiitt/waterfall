# ADR 0003 — Plan-first, gate-driven delivery process

- **Status:** Accepted
- **Date:** 2026-06-30
- **Deciders:** Lead Enterprise Solutions Architect, Senior Product Manager
- **Phase:** 0

## Context
A naive "never stop, never verify, never ask" execution style on a build this size produces drift,
hallucinated providers, fabricated rate limits, and unverifiable architecture. We need autonomy for
speed but hard checkpoints for correctness.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. Plan-first + gates (RESEARCH→BRAINSTORM→PLAN→APPROVAL→IMPLEMENT→VERIFY) with human approval at gates | Prevents drift/hallucination; auditable; still autonomous within a phase | Slower than free-running; needs human at gates | Speed vs correctness |
| B. Free-running autonomous build | Fastest | Drift, fabrication, unverifiable, unsafe | Speed vs trust |
| C. Fully manual, approve every step | Maximum control | Far too slow; defeats automation | Control vs throughput |

## Decision
Adopt the plan-first, gate-driven process encoded in
[`enrichment-discipline`](../skills/enrichment-discipline/SKILL.md): proceed autonomously **within**
a phase; **stop for explicit human approval at every GATE** (end of each planning phase, the
Planning Completion Gate, anything destructive/irreversible, any secret/infra change). No code for a
module before that module has an `APPROVED` plan. Never fabricate; uncited facts are `UNVERIFIED`.

## Rationale
Chose correctness/auditability over raw speed. The gate cost is bounded and buys protection against
the exact failure modes (drift, fabrication) that are most expensive to unwind late.

## Consequences
- Positive: every advance is verifiable; fabrication is structurally discouraged.
- Negative / accepted: human approval is on the critical path at gates. Accepted.
- Follow-ups: the `/gate-check` command + reviewer agents operationalize this.

## Verification
Each gate emits the `enrichment-discipline` checklist; advancing with an unaccepted `FAIL`/
`UNVERIFIED` is itself a process violation caught by `/gate-check`.
