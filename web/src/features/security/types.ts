// features/security/types.ts — local types transcribed from doc 04 §2.1/§2.2/§2.12.
import type { Role } from "../../lib/permissions";

export type { AdminUser, UserStatus } from "../../api/types";

export interface UsersResponse {
  items: import("../../api/types").AdminUser[];
  next_cursor?: string | null;
}

export interface UserInput {
  email: string;
  role: Role;
}

/** Session row (doc 09 §11.1): server enriches the doc 04 §2.1 SessionInfo with ip/agent/last_seen. */
export interface SessionRow {
  id: string;
  user_id: string;
  user_email?: string;
  ip?: string;
  user_agent?: string;
  created_at: string;
  last_seen_at?: string;
  is_current?: boolean;
}

export interface SessionsResponse {
  items: SessionRow[];
}

/** Audit-log row (doc 04 §2.12); row-expand shows before/after jsonb. */
export interface AuditRow {
  seq: number;
  actor?: string;
  actor_user_id?: string;
  action: string;
  object_kind: string;
  object_id: string;
  ip?: string;
  at: string;
  before?: unknown;
  after?: unknown;
}

export interface AuditResponse {
  items: AuditRow[];
  next_cursor?: string | null;
}

/** GET /audit-log/verify (doc 04 §2.12): VERIFIED badge or the first bad seq. */
export interface VerifyResult {
  ok: boolean;
  verified_at?: string;
  first_bad_seq?: number;
}

/** GET /change-history/{kind}/{id} (doc 04 §2.12): per-object timeline (Stripe-style). */
export interface ChangeHistoryEvent {
  at: string;
  kind: string;
  summary: string;
  actor?: string;
}

export interface ChangeHistoryResponse {
  items: ChangeHistoryEvent[];
}

/** GET/PUT /ip-allowlists (doc 04 §2.2): CIDR allowlist; empty disables enforcement. */
export interface IpAllowlistResponse {
  entries: string[];
}
