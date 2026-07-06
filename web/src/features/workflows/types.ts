// features/workflows — local types from the waterfall_workflow JSON Schema (doc 07 §4), the
// config lifecycle (doc 07 §6), and the versioned endpoints (doc 04 §2.7). Shared api/types.ts
// is not edited; config-version types live beside the feature that owns them.
import type { ConfigVersionStatus } from "../../lib/status";

export type Trigger = "api" | "batch" | "webhook";
export type StopCondition = "target-met" | "ceiling" | "exhausted" | "timeout";
export type RetryClass = "TRANSIENT" | "RATE_LIMIT" | "PROVIDER_DOWN";

export interface RetryLogic {
  max_retries?: number;
  backoff_ms?: number;
  retry_on?: RetryClass[];
}

/** waterfall_workflow payload (doc 07 §4). */
export interface WaterfallWorkflowPayload {
  schema_version: 1;
  name: string;
  trigger: Trigger;
  fields: string[];
  entry_provider: string;
  parallel_providers?: string[];
  sequential_providers?: string[];
  retry_logic?: RetryLogic;
  timeout_ms: number;
  confidence_threshold: number;
  min_score?: number;
  max_cost_credits: number;
  max_providers: number;
  fallback_provider?: string;
  stop_conditions: StopCondition[];
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

// ---- config_versions row ----

export interface ConfigVersion {
  id: string;
  kind: "waterfall_workflow";
  scope_key: string;
  version: number;
  status: ConfigVersionStatus;
  parent_version_id?: string | null;
  payload_hash?: string | null;
  payload?: WaterfallWorkflowPayload;
  validation_report?: ValidationReport | null;
  created_at?: string;
  published_at?: string | null;
}

/** Denormalized workflow_index list item (GET /workflows). */
export interface WorkflowIndexItem {
  scope_key: string;
  name: string;
  trigger: Trigger;
  active_version?: number | null;
  fields?: string[];
}

// ---- dry-run (doc 07 §7) ----

export interface DryRunStep {
  provider: string;
  cost_credits: number;
  expected_confidence: number;
}

export interface EffectiveOverride {
  effective: string;
  source: string;
  source_version?: number;
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

// ---- publish / rollback envelopes ----

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
