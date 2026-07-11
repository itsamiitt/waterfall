// features/intent/types.ts — Intent dashboard read models (docs/research-intelligence/08).
// These per-class computed scores are the explainable model output. They are DISTINCT from the
// single-valued canonical intent Fields (intent_score / buying_signal / intent_topics) that the
// engine writes back to enrichment — the two are never conflated (doc 05 methodology).

/** One account with its strongest computed intent class (list row). Mirrors dash/intent AccountSummary. */
export interface AccountSummary {
  account: string;
  top_class: string;
  top_score: number;
  classes: number;
}

/** One per-class computed intent score (detail row). Mirrors dash/intent AccountScore. */
export interface AccountScore {
  class: string;
  score: number;
  confidence: number;
  signal_count: number;
  config_version: string;
  computed_at: string;
}

/** GET /intent/accounts envelope. */
export interface AccountsResponse {
  items: AccountSummary[];
}

/** GET /intent/accounts/{domain} envelope. */
export interface AccountResponse {
  account: string;
  scores: AccountScore[];
}
