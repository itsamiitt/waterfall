# 15 — Verification Engine

**Status:** `IN-REVIEW` · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Gated by:** [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) · [api-integration](../skills/api-integration/SKILL.md) · `/architecture-review`

> Verification is a **cheap terminal gate** in the waterfall (ADR-0005/0007), not a sourcing step. It
> raises/lowers confidence and can re-open the waterfall for a field. Providers confirmed in [`03`](03-Provider-Research.md).

## 1. Email verification → `email_status`
- Providers: **ZeroBounce, NeverBounce, Kickbox, Emailable, Clearout** (+ Melissa/Ekata as risk); many
  finders (Findymail, Snov, Enrow, Prospeo) include built-in verify.
- Checks: syntax, MX/domain, mailbox/SMTP, **catch-all detection**, role/disposable flags.
- Status enum: `valid | invalid | catch_all | role | disposable | unknown`.

## 2. Phone verification → `phone_status`
- Providers: **Twilio Lookup, Telnyx** (+ Melissa/Ekata).
- Checks: line type (mobile/landline/VoIP), carrier, reachability, ported status.
- Status enum: `mobile | landline | voip | invalid | unknown`.

## 3. Role in the waterfall (ADR-0005)
- Placed **last** as the deliverability/validity gate before a value is emitted or billed.
- A verifier result **adjusts calibrated confidence**: e.g. `invalid` → drop the value + **re-open** the
  waterfall for that field; `valid` → boost confidence toward the SPRT target.
- Cheap verify can **gate** expensive find calls (Hunter's 0.5-credit verify pattern, `01`).

## 4. Discipline
Idempotent (G2), bounded (G3), cost-checked (G4), provenance on the status + which verifier produced it (G5).

## 5. Open items
| ID | Item | Status |
|----|------|--------|
| VE-1 | Verification providers | ✅ confirmed (`03`) |
| VE-2 | `email_status`/`phone_status` enums | ✅ §1/§2 |
| VE-3 | Confidence adjustment from verification result | ✅ §3 (formalize weights at impl) |

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Providers cited (`03`) | PASS |
| Verify as gate, not source (ADR-0005/0007) | PASS |
| Status enums defined | PASS |
| G2–G5 applied | PASS |
| Failed verify re-opens waterfall | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
