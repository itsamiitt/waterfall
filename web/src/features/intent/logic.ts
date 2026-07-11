// features/intent/logic.ts — pure helpers for the Intent surface (unit-tested without hooks).
import type { AccountScore } from "./types";

/** The strongest computed class for an account (max score; ties keep the first seen), or null when
 * there are none. This surfaces ONE class for the headline tile — it does NOT collapse the per-class
 * scores into a single value; the full list is always rendered separately (doc 05). */
export function strongestClass(scores: readonly AccountScore[]): AccountScore | null {
  let best: AccountScore | null = null;
  for (const s of scores) if (!best || s.score > best.score) best = s;
  return best;
}
