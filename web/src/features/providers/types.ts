// features/providers/types.ts — module DTOs transcribed from doc 04 §2.3 (kept feature-local
// per the P9 brief so sibling agents never collide on shared api/types.ts). Field names mirror
// the doc 04 example bodies and doc 03 columns verbatim.
import type { InclusionStatus, OpState } from "../../lib/status";

/** One declared capability row (doc 04 §2.3 `capabilities[]`). */
export interface ProviderCapability {
  field: string;
  cost_credits: number;
  expected_confidence: number;
}

/** Catalog row from GET /providers (list) and GET /providers/{id} (detail, full for O). The
 * dual axes are ALWAYS separate: `status` is the ADR-0009 inclusion trichotomy (outlined chip),
 * `op_state` the runtime state (filled chip); `effective_available` is server-computed — the
 * client NEVER derives it (doc 04 §2.3). */
export interface Provider {
  id: string;
  display_name: string;
  category?: string;
  status: InclusionStatus | string;
  op_state?: OpState | string;
  effective_available: boolean;
  /** Names the failed conjunct when unavailable, e.g. "op_state_paused" (doc 04 §2.3). */
  unavailable_reason: string | null;
  compliance_review_status?: string;
  capabilities?: ProviderCapability[];
  health_score?: number;
  avg_latency_ms?: number;
  credits_remaining?: number;
  priority?: number;
  sunset_at?: string | null;
  region?: string[];
  tags?: string[];
  // Integration descriptor / ops (doc 04 §2.3 create body; PATCH-writable subset).
  base_url?: string;
  api_version?: string;
  auth_scheme?: string;
  auth_header?: string;
  timeout_ms?: number;
  rate_limit_rpm?: number;
  concurrency_limit?: number;
  daily_limit?: number;
  monthly_limit?: number;
  breaker_threshold?: number;
  breaker_cooldown_s?: number;
  unit_cost_credits?: number;
  retry_policy?: { max_attempts?: number; backoff_ms?: number };
  created_at?: string;
  updated_at?: string;
  archived_at?: string | null;
}

/** List filter params (doc 04 §2.3 whitelist). */
export interface ProviderFilter {
  status?: string;
  op_state?: string;
  category?: string;
  region?: string;
  tag?: string;
  q?: string;
}

/** GET /providers/{id}/health summary (doc 04 §2.3). */
export interface ProviderHealth {
  provider_id: string;
  health: string;
  health_score?: number;
  uptime_pct?: number;
  p95_ms?: number;
  p99_ms?: number;
  last_error_class?: string | null;
  last_checked_at?: string | null;
}

/** GET /providers/{id}/stats point (doc 04 §2.3; per-error-class failure columns). */
export interface ProviderStatsPoint {
  ts: string;
  requests?: number;
  successes?: number;
  failures?: number;
  p50_ms?: number;
  p95_ms?: number;
  p99_ms?: number;
  [errorClassOrExtra: string]: string | number | null | undefined;
}

export interface ProviderStats {
  points: ProviderStatsPoint[];
}

/** GET /change-history/provider/{id} row (doc 04 §2.3 history tab: versions + approvals + audit). */
export interface ChangeHistoryEvent {
  id: string;
  at: string;
  kind: string;
  actor?: string;
  summary: string;
}

/** GET /providers/compare + coverage + rankings (doc 04 §2.3). */
export interface CompareCell {
  field: string;
  declared_cost_credits?: number;
  declared_confidence?: number;
  measured_hit_rate?: number;
  measured_p95_ms?: number;
  measured_cost_per_hit?: number;
}

export interface CompareProvider {
  provider_id: string;
  cells: CompareCell[];
}

export interface CompareResult {
  fields: string[];
  providers: CompareProvider[];
}

export interface RankingRow {
  field: string;
  provider_id: string;
  cost_per_hit: number;
}

/** GET /meta/enums — closed vocabularies keyed by name (doc 04 §2.13). Shape is a map of
 * enum-name → values so the ProviderConfigForm's selects are server-driven, not hard-coded. */
export type MetaEnums = Record<string, string[]>;
