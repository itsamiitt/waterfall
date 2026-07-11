// features/airesearch/types.ts — Research dashboard read models (docs/research-intelligence/06, 08).
// The list is lightweight summaries; the detail is the full stored Dossier document (an arbitrary
// composite JSON referencing canonical Fields — schema in doc 06), rendered read-only.

/** One dossier summary (list row). Mirrors dash/research DossierSummary. */
export interface DossierSummary {
  dossier_id: string;
  subject_key: string;
  overall_confidence: number;
  config_version: string;
  freshness_at: string;
}

/** GET /research/dossiers envelope. */
export interface DossiersResponse {
  items: DossierSummary[];
}

/** The full stored Dossier document — an arbitrary JSON object (top-level keys per doc 06:
 * company_profile, firmographics, intent, provenance, …). Rendered as-is; never reshaped here. */
export type DossierDoc = Record<string, unknown>;
