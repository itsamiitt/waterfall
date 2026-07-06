// features/routing — local types transcribed from the routing_policy JSON Schema (doc 07 §2),
// the config lifecycle (doc 07 §6), and the versioned endpoints (doc 04 §2.7). Shared
// api/types.ts is NOT edited (doc 12 P8 OI-P8-1); config-version types live here beside the
// feature that owns them.
import type { ConfigVersionStatus } from "../../lib/status";

export type TriMode = "inherit" | "off" | "on";

/** Per-Provider tri-state override (doc 07 §2). `inherit` is transparent to precedence. */
export interface ProviderOverride {
  mode: TriMode;
  priority?: number;
  key_pool?: string;
}

export interface RoutingWaterfall {
  /** Explicit Provider priority order override (maxItems 16). */
  order?: string[];
  /** Bounded cheap prefix fanned out concurrently (2–4 members, VR-6). */
  parallel_group?: { providers: string[] };
  /** Strict-order chains (e.g. finder → verifier). */
  sequential_chains?: string[][];
  retry_order?: string[];
  failover_order?: string[];
}

export interface RoutingThresholds {
  confidence_target?: number;
  min_confidence?: number;
  max_cost_credits_per_field?: number;
  max_cost_credits_per_record?: number;
}

/** routing_policy payload (doc 07 §2). */
export interface RoutingPolicyPayload {
  schema_version: 1;
  scope?: { tenant?: string; product?: string; country?: string };
  provider_overrides?: Record<string, ProviderOverride>;
  waterfall?: RoutingWaterfall;
  thresholds?: RoutingThresholds;
}

// ---- validation report (doc 07 §5) ----

export interface ValidationEntry {
  rule: string;
  code: string;
  severity: "error" | "warning";
  path: string;
  message: string;
}

export interface ValidationReport {
  validated_at: string;
  payload_hash: string;
  errors: ValidationEntry[];
  warnings: ValidationEntry[];
}

// ---- config_versions row (doc 04 §2.7) ----

export interface ConfigVersion {
  id: string;
  kind: "routing_policy";
  scope_key: string;
  version: number;
  status: ConfigVersionStatus;
  parent_version_id?: string | null;
  payload_hash?: string | null;
  payload?: RoutingPolicyPayload;
  validation_report?: ValidationReport | null;
  created_by?: string;
  created_at?: string;
  published_at?: string | null;
}

/** Effective override for one Provider, as returned by the resolver (never client-derived). */
export interface EffectiveOverride {
  effective: TriMode | string;
  source: string;
  source_version?: number;
}

/** GET /routing scope summary — active version + epoch + resolver effective values. */
export interface RoutingScopeSummary {
  scope_key: string;
  active_version: number | null;
  epoch: number;
  overrides?: Record<string, EffectiveOverride>;
}

export interface RoutingScopeList {
  scopes: RoutingScopeSummary[];
}

// ---- dry-run (doc 07 §7, doc 04 §2.7) ----

export interface DryRunStep {
  provider: string;
  cost_credits: number;
  expected_confidence: number;
}

export interface DryRunResult {
  zero_egress: boolean;
  resolved_scope: {
    levels_consulted: string[];
    overrides: Record<string, EffectiveOverride>;
  };
  by_field: Record<string, DryRunStep[]>;
  max_committed_credits: number;
  stop_projection: { condition: string; expected_providers_used: number };
  warnings: ValidationEntry[];
  diff_vs_active?: { provider_order_changed: boolean; removed: string[]; added: string[] };
}

// ---- publish / rollback envelopes (doc 04 §2.7) ----

export interface PublishResult {
  kind: string;
  scope_key: string;
  active_version_id: string;
  version: number;
  epoch: number;
  published_at: string;
}

export interface ApprovalPending {
  approval_request_id: string;
}

export type PublishResponse = PublishResult | ApprovalPending;

export function isApprovalPending(r: PublishResponse): r is ApprovalPending {
  return "approval_request_id" in r;
}
