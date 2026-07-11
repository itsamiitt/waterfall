// features/aimodels/types.ts — the LLM model cascade catalog (docs/research-intelligence/04, 08).
// A read-only projection of the platform ai.Models registry; identical for every caller. Per-token
// costs are UNVERIFIED placeholders that only set the free→paid cascade order (doc 11).

/** One LLM registry entry, projected for the dashboard. Mirrors dash/airouting ModelInfo. */
export interface ModelInfo {
  slug: string;
  model_id: string;
  dialect: string;
  host: string;
  status: string;
  free: boolean;
  in_per_mtok: number;
  out_per_mtok: number;
  docs_url: string;
}

/** GET /ai/models envelope. */
export interface ModelsResponse {
  items: ModelInfo[];
}
