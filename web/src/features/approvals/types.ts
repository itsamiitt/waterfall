// features/approvals/types.ts — local types transcribed from doc 04 §2.12 (approvals).
export interface ApprovalDecision {
  approver_user_id: string;
  approver_email?: string;
  decision: "approve" | "reject";
  comment: string;
  mfa_verified: boolean;
  created_at: string;
}

export interface ApprovalRequest {
  id: string;
  action_kind: string;
  status: "pending" | "approved" | "rejected" | "expired" | "cancelled" | "executed" | "failed";
  required_approvals: number;
  decisions: ApprovalDecision[];
  requested_by?: string;
  requested_by_user_id?: string;
  created_at?: string;
  expires_at: string;
  executed_at?: string | null;
  execution_result?: unknown;
  /** Detail artifacts (doc 09 §11.1 REVIEW panel): the pinned payload + review evidence. */
  payload?: unknown;
  diff?: unknown;
  validation_report?: unknown;
  dry_run?: unknown;
}

export interface ApprovalsResponse {
  items: ApprovalRequest[];
  next_cursor?: string | null;
}

/** The decision body + step-up header sent to approve/reject (doc 04 §2.12). */
export interface DecisionRequest {
  body: { comment: string };
  headers: Record<string, string>;
}
