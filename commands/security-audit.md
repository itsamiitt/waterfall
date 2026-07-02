# Command: /security-audit

**Runs:** [Security Auditor](../agents/security-auditor.md) · **Skill:** [waterfall-correctness](../skills/waterfall-correctness/SKILL.md)

## Purpose
Run tenant-isolation, IDOR, **SSRF**, secret-handling, and data-residency checks on a document or
module. Tenant isolation + SSRF are the two highest-priority areas.

## Usage
`/security-audit <doc path | module>`

## Steps
1. Map the data flow + every query, cache key, queue message, log line, and outbound fetch.
2. Run the Security Auditor Priority-1 checks (tenant isolation, SSRF) then the rest.
3. For each finding: severity, location, exploit sketch, concrete fix.
4. Write findings into `18-Security.md` (or the module's security section) + the doc's Open items.
5. Update `CHANGELOG.md`. `GATE-PASS` only if no unaccepted crit/high.

## Output (written to repo)
- A findings table (severity/issue/location/exploit/fix).
- Pass/fail per checklist item; gate recommendation.

## Pass criteria
- No unaccepted crit/high; tenant_id from authenticated principal only; egress allow-list enforced;
  no secrets in code/logs; data residency honored.
