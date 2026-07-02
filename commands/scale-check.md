# Command: /scale-check

**Runs:** [Cost/Scale Reviewer](../agents/cost-scale-reviewer.md)

## Purpose
Validate throughput targets, per-record cost math, and queue/back-pressure/autoscaling design with
shown math.

## Usage
`/scale-check <doc path | phase number>`

## Steps
1. Pull the throughput target + math (`00` §4), routing/exec (`08`/`09`), queue (`10`), scaling
   (`11`), cost (`16`), provider prices (`03`).
2. Recompute each capacity/cost claim (Little's Law, calls/record, partition/concurrency math).
3. Mark each `PASS`/`FAIL`/`UNVERIFIED` with the assumption + the load test needed to verify it.
4. Recommend worker/partition/concurrency/DB sizing + required cache hit-rate.
5. Write the validation table into `11`/`16` + Open items; update `CHANGELOG.md`.

## Output (written to repo)
- Capacity/cost validation table with shown math.
- Sizing recommendations + list of load tests to run.

## Pass criteria
- Every capacity/cost PASS shows math; numbers depending on uncited provider data stay `UNVERIFIED`;
  retries can't push cost past the ceiling; autoscaling has finite caps.
