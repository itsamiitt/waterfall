// features/alerts/vocab.ts — the CLOSED alert-rule metric vocabulary (doc 10 §4, 17 entries).
// The rule editor is built from PICKERS over this table — there is NO query language and NO free
// metric box (doc 04 §2.11: metric outside vocab → 422). The SPA mirror is parity-checked against
// GET /v1/admin/meta/enums; this table is the source of truth for the editor's options + defaults.

export type AlertOp = "lt" | "lte" | "gt" | "gte";

export interface OpSpec {
  value: AlertOp;
  label: string;
}

/** Closed comparison operators (doc 04 §2.11 example uses `gt`; doc 10 §4 defaults use lt/gt/gte). */
export const OPS: readonly OpSpec[] = [
  { value: "lt", label: "< (less than)" },
  { value: "lte", label: "≤ (at most)" },
  { value: "gt", label: "> (greater than)" },
  { value: "gte", label: "≥ (at least)" },
];

export const SEVERITIES = ["critical", "warning", "info"] as const;
export type Severity = (typeof SEVERITIES)[number];

/** Channel kinds (doc 04 §2.11 POST /alerts/channels). */
export const CHANNEL_KINDS = ["email", "slack", "teams", "discord", "webhook"] as const;
export type ChannelKind = (typeof CHANNEL_KINDS)[number];

export type ScopeKey = "provider_id" | "pool_id" | "queue" | "kind" | "workflow_key" | "scope" | "scope_key" | "period";

export interface MetricSpec {
  metric: string;
  unit: string;
  /** Scope-key inputs the editor renders (empty = platform-scoped, operator-only). */
  scopeKeys: readonly ScopeKey[];
  defaultOp: AlertOp;
  defaultThreshold: number | null;
  defaultWindowS: number | null;
  /** Point-in-time metrics: window_s is accepted but ignored — the editor greys it out (doc 10 §4). */
  windowApplies: boolean;
  /** system.* rules live under tenant_id='platform' and are operator-only (doc 10 §4). */
  platformOnly?: boolean;
}

/** The exactly-17 metric vocabulary (doc 10 §4). Ordered as the spec table. */
export const METRIC_VOCAB: readonly MetricSpec[] = [
  { metric: "provider.success_rate", unit: "ratio 0..1", scopeKeys: ["provider_id"], defaultOp: "lt", defaultThreshold: 0.9, defaultWindowS: 600, windowApplies: true },
  { metric: "provider.error_rate", unit: "ratio 0..1", scopeKeys: ["provider_id"], defaultOp: "gt", defaultThreshold: 0.05, defaultWindowS: 600, windowApplies: true },
  { metric: "provider.p95_latency_ms", unit: "ms", scopeKeys: ["provider_id"], defaultOp: "gt", defaultThreshold: 5000, defaultWindowS: 600, windowApplies: true },
  { metric: "provider.credits_remaining", unit: "credits", scopeKeys: ["provider_id"], defaultOp: "lt", defaultThreshold: 10000, defaultWindowS: null, windowApplies: false },
  { metric: "key.credits_remaining", unit: "credits", scopeKeys: ["provider_id", "pool_id"], defaultOp: "lt", defaultThreshold: 1000, defaultWindowS: null, windowApplies: false },
  { metric: "key.consecutive_failures", unit: "count", scopeKeys: ["provider_id", "pool_id"], defaultOp: "gte", defaultThreshold: 5, defaultWindowS: null, windowApplies: false },
  { metric: "key.active_ratio_in_pool", unit: "ratio 0..1", scopeKeys: ["pool_id"], defaultOp: "lt", defaultThreshold: 0.5, defaultWindowS: null, windowApplies: false },
  { metric: "queue.depth", unit: "jobs", scopeKeys: ["queue"], defaultOp: "gt", defaultThreshold: 10000, defaultWindowS: 300, windowApplies: true },
  { metric: "queue.oldest_age_s", unit: "seconds", scopeKeys: ["queue"], defaultOp: "gt", defaultThreshold: 900, defaultWindowS: 300, windowApplies: true },
  { metric: "queue.dead_count", unit: "jobs", scopeKeys: ["queue"], defaultOp: "gt", defaultThreshold: 50, defaultWindowS: 300, windowApplies: true },
  { metric: "worker.lost_count", unit: "workers", scopeKeys: ["kind", "queue"], defaultOp: "gt", defaultThreshold: 0, defaultWindowS: 120, windowApplies: true },
  { metric: "worker.heartbeat_age_s", unit: "seconds", scopeKeys: ["kind", "queue"], defaultOp: "gt", defaultThreshold: 60, defaultWindowS: 120, windowApplies: true },
  { metric: "cost.daily_credits", unit: "credits", scopeKeys: ["provider_id", "workflow_key"], defaultOp: "gt", defaultThreshold: null, defaultWindowS: null, windowApplies: false },
  { metric: "cost.budget_burn_pct", unit: "percent", scopeKeys: ["scope", "scope_key", "period"], defaultOp: "gte", defaultThreshold: 80, defaultWindowS: null, windowApplies: false },
  { metric: "cost.anomaly", unit: "credits", scopeKeys: ["provider_id", "workflow_key"], defaultOp: "gt", defaultThreshold: 50, defaultWindowS: null, windowApplies: false },
  { metric: "system.sse_clients", unit: "clients", scopeKeys: [], defaultOp: "gt", defaultThreshold: 400, defaultWindowS: 120, windowApplies: true, platformOnly: true },
  { metric: "system.aggregator_lag_s", unit: "seconds", scopeKeys: [], defaultOp: "gt", defaultThreshold: 30, defaultWindowS: 120, windowApplies: true, platformOnly: true },
];

export const METRIC_BY_ID = new Map(METRIC_VOCAB.map((m) => [m.metric, m]));
