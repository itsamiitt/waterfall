// features/keys/types.ts — Module 3 DTOs (doc 04 §2.4), kept feature-local. No field ever
// carries a secret: only secret_last4 + fingerprint_prefix exist (write-only secrets, §2.4).
import type { KeyStatus } from "../../lib/status";

/** Health is a small enum distinct from the KM-3 `status` (doc 09 §3.1: ok/warn/err/unknown). */
export type KeyHealth = "ok" | "warn" | "err" | "error" | "unknown" | string;

export interface ProviderKey {
  id: string;
  provider_id: string;
  label: string;
  status: KeyStatus | string;
  health: KeyHealth;
  secret_last4?: string;
  fingerprint_prefix?: string;
  pool?: string | null;
  pool_id?: string | null;
  region?: string;
  environment?: string;
  weight?: number;
  priority?: number;
  credits_remaining?: number;
  credits_limit?: number;
  usage_today?: number;
  success_ewma?: number;
  latency_ewma_ms?: number;
  expires_at?: string | null;
  last_used_at?: string | null;
  consecutive_failures?: number;
  error_counters?: Record<string, number>;
  rotation_group?: string | null;
  imported_batch_id?: string | null;
  daily_limit?: number;
  monthly_limit?: number;
  rpm_limit?: number;
  tags?: string[];
  created_at?: string;
  updated_at?: string;
}

/** The doc 04 §2.4 filter whitelist — 1:1 with the grid's dropdowns; there is NO free-text q
 * (doc 09 §3.1: unknown param → 400 invalid_filter). */
export interface KeyFilter {
  status?: string[];
  health?: string[];
  region?: string;
  environment?: string;
  tag?: string;
  rotation_group?: string;
  imported_batch_id?: string;
  pool_id?: string;
}

/** Bulk request scope: ids OR filter, never both (doc 04 §4.2). */
export interface BulkRequest {
  provider_id: string;
  ids?: string[];
  filter?: KeyFilter & { provider_id: string };
  op: BulkOp;
  reason?: string;
  preview?: boolean;
}
export type BulkOp = "enable" | "disable" | "pause" | "rotate" | "delete";

export interface PreviewCount {
  matched: number;
}

/** §4.3 progress schema (shared by /bulk-jobs/{id} and /key-imports/{job_id}). */
export interface JobProgress {
  job_id: string;
  kind: string;
  status: "queued" | "running" | "succeeded" | "partial" | "failed" | string;
  total: number;
  succeeded: number;
  failed: number;
  started_at?: string | null;
  finished_at?: string | null;
  matched_at_execution?: number | null;
  errors?: JobRowError[];
  error_summary?: Record<string, number>;
  errors_truncated?: boolean;
}
export interface JobRowError {
  row: number | null;
  id: string | null;
  code: string;
  message: string;
}

export interface RotateResult {
  successor_key_id: string;
  old_key_id: string;
  old_key_status: string;
  overlap_until: string;
}
