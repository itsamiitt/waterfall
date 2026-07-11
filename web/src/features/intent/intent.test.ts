// features/intent unit tests: the strongest-class selector picks the max score and never collapses
// the per-class scores into a single number (doc 05 — the ten Intent Class Scores are distinct from
// the single intent_score Field written back to enrichment).
import { describe, expect, it } from "vitest";
import { strongestClass } from "./logic";
import type { AccountScore } from "./types";

const score = (cls: string, s: number): AccountScore => ({
  class: cls,
  score: s,
  confidence: 0.5,
  signal_count: 1,
  config_version: "v1",
  computed_at: "2026-07-11T00:00:00Z",
});

describe("intent strongestClass", () => {
  it("returns null for no scores", () => {
    expect(strongestClass([])).toBeNull();
  });

  it("picks the highest-scoring class", () => {
    const top = strongestClass([score("hiring", 0.4), score("buying", 0.8), score("cloud_migration", 0.6)]);
    expect(top?.class).toBe("buying");
    expect(top?.score).toBe(0.8);
  });

  it("keeps every class distinct — the headline tile does not replace the list", () => {
    const all = [score("hiring", 0.4), score("buying", 0.8)];
    expect(strongestClass(all)?.class).toBe("buying");
    expect(all).toHaveLength(2); // source list untouched
  });
});
