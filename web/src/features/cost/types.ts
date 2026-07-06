// features/cost/types.ts — local types transcribed from doc 04 §2.10 (cost analytics + budgets).
// All figures are `source:"modeled"` from rate cards; nothing is derived client-side (doc 08 §4).

/** The closed group-by dimensions (doc 04 §2.10). `key` is operator-only server-side. */
export const GROUP_BYS = ["provider", "key", "tenant", "workflow", "country"] as const;
export type GroupBy = (typeof GROUP_BYS)[number];

/** Dimension → the item field carrying that dimension's identifier (doc 04 §2.10 example). */
export const DIM_FIELD: Record<GroupBy, string> = {
  provider: "provider_id",
  key: "key_id",
  tenant: "tenant_id",
  workflow: "workflow_key",
  country: "country",
};

export interface CostFilters {
  group_by: GroupBy;
  from?: string;
  to?: string;
  /** Active drill-down filters, dim → value (rendered as `filter[dim]=value`). */
  filter: Record<string, string>;
}

export interface CostItem {
  credits: number;
  calls: number;
  successful_results: number;
  credits_per_call: number;
  credits_per_successful_result: number;
  // one of these dimension fields is present depending on group_by
  provider_id?: string;
  key_id?: string;
  tenant_id?: string;
  workflow_key?: string;
  country?: string;
  [extra: string]: string | number | undefined;
}

export interface CostSummary {
  group_by: GroupBy;
  from: string;
  to: string;
  source: "modeled";
  items: CostItem[];
  next_cursor: string | null;
}

/** GET /cost/per-enrichment — numerator + denominator carried together (doc 04 §2.10). */
export interface PerEnrichment {
  source: "modeled";
  credits: number;
  calls: number;
  successful_results: number;
  credits_per_call: number;
  credits_per_successful_result: number;
}

/** GET /cost/forecast — linear / 7d-seasonal projection with an ~80% band, or a collecting state. */
export interface CostForecast {
  method: "linear" | "seasonal_7d" | "insufficient_history";
  source: "modeled";
  /** end-of-month projected credits (absent when insufficient_history). */
  eom_credits?: number;
  /** ~80% indicative band bounds (absent when insufficient_history). */
  band_low?: number;
  band_high?: number;
  /** how many days of history exist (drives the "N/14 days" collecting copy). */
  days_of_history?: number;
}

/** GET/PUT /budgets item (doc 04 §2.10). PUT is a full replacement of the set. */
export interface BudgetItem {
  scope: "tenant" | "provider" | "workflow";
  scope_key: string;
  period: "day" | "month";
  limit_credits: number;
  alert_pct: number[];
  current_period_start?: string;
  consumed_credits?: number;
}

export interface BudgetsResponse {
  items: BudgetItem[];
}
