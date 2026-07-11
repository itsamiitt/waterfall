// features/aimodels unit tests: the cost label distinguishes free-pool models from paid ones and
// renders the placeholder per-Mtok figures verbatim (doc 11 — UNVERIFIED, cascade-ordering only).
import { describe, expect, it } from "vitest";
import { modelCost } from "./logic";

describe("aimodels modelCost", () => {
  it("labels free-pool models as free", () => {
    expect(modelCost({ free: true, in_per_mtok: 0, out_per_mtok: 0 })).toBe("free");
  });

  it("shows the placeholder per-Mtok cost for paid models", () => {
    expect(modelCost({ free: false, in_per_mtok: 150, out_per_mtok: 600 })).toBe("150 / 600 cr per Mtok");
  });
});
