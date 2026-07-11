// features/aimodels/logic.ts — pure helpers for the AI models catalog (unit-tested without hooks).
import type { ModelInfo } from "./types";

/** A compact cost label for a model: "free" for no-cost pool models, else "<in> / <out> cr per Mtok".
 * The per-Mtok figures are UNVERIFIED placeholders that only set the cascade's free→paid order and the
 * G4 accounting scale — never a billed price (doc 11). */
export function modelCost(m: Pick<ModelInfo, "free" | "in_per_mtok" | "out_per_mtok">): string {
  if (m.free) return "free";
  return `${m.in_per_mtok} / ${m.out_per_mtok} cr per Mtok`;
}
