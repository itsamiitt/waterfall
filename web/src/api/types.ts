// api/types.ts — HAND-AUTHORED types for the doc 04 surfaces P8 touches.
//
// Doc 12 P8 specifies `types.gen.ts` generated from openapi-admin.yaml, but that yaml is
// deferred (doc 04 OI-API-1) until the P7 streams module ships it. These types are therefore
// transcribed by hand from doc 04 §1 (conventions), §2.1 (auth), §2.2 (users/roles), §2.13
// (overview/streams/search) and §3 (SSE), cross-checked against the live handlers in
// internal/dash/httpx. Recorded as doc 12 OI-P8-1; codegen replaces this file's server-shape
// section when openapi-admin.yaml lands, and feature types for P9–P11 extend the placeholder
// section at the bottom.

import type { Role } from "../lib/permissions";
export type { Role } from "../lib/permissions";

// ---- §1.6 uniform error envelope ----

export interface ErrorEnvelope {
  error: {
    code: string;
    message: string;
  };
}

// ---- §1.4 cursor page envelope (re-exported seam; helpers live in lib/cursors.ts) ----

export type { Page } from "../lib/cursors";

// ---- §1.7 async 202 envelopes (discriminate on the field name) ----

export interface JobAccepted {
  job_id: string;
}

export interface ApprovalAccepted {
  approval_request_id: string;
}

export type Accepted = JobAccepted | ApprovalAccepted;

export function isApprovalAccepted(a: Accepted): a is ApprovalAccepted {
  return "approval_request_id" in a;
}

// ---- §2.1 auth and sessions ----

export interface UserSummary {
  id: string;
  email: string;
  role: Role;
  tenant_id: string;
  mfa_enrolled: boolean;
}

export interface LoginRequest {
  email: string;
  password: string;
}

/** POST /auth/login | /auth/mfa/verify: "mfa_required" carries no token; "ok" starts the
 * CSRF-bearing session. */
export type SessionResponse =
  | { status: "mfa_required" }
  | { status: "ok"; csrf_token: string; user: UserSummary };

export interface MfaVerifyRequest {
  code: string;
}

/** GET /auth/me — SPA bootstrap (doc 04 §2.1). csrf_token is optional: doc 08 §7 plans
 * rehydration through this call; the current handler does not return it yet. */
export interface AuthMe {
  user: UserSummary;
  role: Role;
  tenant_id: string;
  csrf_token?: string;
}

/** POST /auth/mfa/enroll — provisioning URI returned exactly once. */
export interface MfaEnrollResponse {
  otpauth_url: string;
}

/** POST /auth/mfa/enroll/confirm — recovery codes returned exactly once. */
export interface MfaConfirmResponse {
  recovery_codes: string[];
}

export interface SessionInfo {
  id: string;
  user_id: string;
  created_at: string;
  idle_expires_at: string;
  absolute_expires_at: string;
  mfa_verified: boolean;
}

// ---- §2.2 users ----

export type UserStatus = "active" | "disabled";

export interface AdminUser {
  id: string;
  email: string;
  role: Role;
  status: UserStatus;
  tenant_id: string;
  mfa_enrolled: boolean;
  created_at: string;
}

// ---- §2.13 overview / search ----

/** One overview tile's values. Tiles carry heterogeneous shapes (doc 04 §2.13 example +
 * doc 09 §1.2 vocabulary); all fields are optional and rendered verbatim — never derived. */
export interface TileValue {
  value?: number;
  delta_pct?: number;
  of?: number;
  value_s?: number;
  queue?: string;
  budget_pct?: number;
  critical?: number;
  warning?: number;
  [extra: string]: number | string | undefined;
}

export interface OverviewSnapshot {
  generated_at: string;
  tiles: Record<string, TileValue>;
}

export type SearchKind =
  | "provider"
  | "key"
  | "pool"
  | "workflow"
  | "worker"
  | "queue"
  | "user";

export interface SearchResult {
  kind: SearchKind;
  id: string;
  label: string;
  match_field: string;
}

// ---- §3 SSE contract ----

/** Canonical singular topic vocabulary (doc 04 §3.2). */
export const SSE_TOPICS = [
  "overview",
  "provider",
  "key",
  "queue",
  "worker",
  "alert",
  "import",
  "approval",
] as const;
export type SseTopic = (typeof SSE_TOPICS)[number];

/** Every event's `data` field (doc 04 §3.3). */
export interface SseEnvelope<P = unknown> {
  v: number;
  ts: string;
  scope: Record<string, string>;
  payload: P;
}

/** `event: reset` control payload (doc 04 §3.5): refetch snapshots for these topics. */
export interface SseResetPayload {
  v: number;
  topics: SseTopic[];
}

// ---- Placeholder module types (P9–P11 extend these; kept minimal so api/keys.ts and the
// route stubs typecheck today) ----

export interface ProviderRef {
  id: string;
  display_name?: string;
  status?: string;
  op_state?: string;
  effective_available?: boolean;
}

export interface ProviderKeyRef {
  id: string;
  provider_id: string;
  status?: string;
  label?: string;
}

export interface KeyPoolRef {
  id: string;
  name?: string;
}

export interface QueueRef {
  name: string;
  oldest_age_s?: number;
}

export interface WorkerRef {
  id: string;
  status?: string;
  desired_state?: string;
}

export interface AlertEventRef {
  id: string;
  rule_id?: string;
  state?: string;
  severity?: string;
}

export interface ApprovalRequestRef {
  id: string;
  action_kind?: string;
  status?: string;
}

export interface ImportBatchRef {
  id: string;
  succeeded?: number;
  failed?: number;
  total?: number;
}
