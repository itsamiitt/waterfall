// features/airesearch/logic.ts — pure helpers for the Research surface (unit-tested without hooks).
import type { DossierDoc } from "./types";

/** A human headline for a Dossier: the company name when present, else the subject key, else null.
 * Defensive — the Dossier is arbitrary JSON, so every access is type-guarded (doc 06). */
export function dossierHeadline(doc: unknown): string | null {
  if (!doc || typeof doc !== "object") return null;
  const d = doc as DossierDoc;
  const cp = d.company_profile;
  if (cp && typeof cp === "object") {
    const name = (cp as Record<string, unknown>).name;
    if (typeof name === "string" && name !== "") return name;
  }
  if (typeof d.subject_key === "string" && d.subject_key !== "") return d.subject_key;
  return null;
}
