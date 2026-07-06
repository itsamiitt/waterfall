// features/approvals/api.ts — the ONLY place approval endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §14 row 36):
//   ApprovalsInbox   GET /approvals
//   ApprovalDetail   GET /approvals/{id}
//   Approve/Reject   POST /approvals/{id}/approve|reject  (Idempotency-Key + X-MFA-Code step-up)
//   Cancel           POST /approvals/{id}/cancel
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { post } from "../../api/client";
import { get } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import { listQuery } from "../../lib/cursors";
import type { ApprovalRequest, ApprovalsResponse, DecisionRequest } from "./types";

export interface ApprovalFilters {
  status?: string;
  action_kind?: string;
}

/** Build the decision body + step-up header for approve/reject (doc 04 §2.12). Pure + tested:
 * the TOTP code MUST travel as the `X-MFA-Code` header, never in the JSON body. */
export function buildDecisionRequest(comment: string, mfaCode: string): DecisionRequest {
  return { body: { comment }, headers: { "X-MFA-Code": mfaCode } };
}

export function useApprovals(filters: ApprovalFilters) {
  const query = listQuery({ status: filters.status ?? "pending", action_kind: filters.action_kind });
  return useQuery({
    queryKey: [...qk.approvals.root, query] as const,
    queryFn: () => get<ApprovalsResponse>(`/approvals${query}`),
    staleTime: staleTimes.config,
  });
}

export function useApproval(id: string | undefined) {
  return useQuery({
    queryKey: id ? qk.approvals.detail(id) : ["approvals", "detail", ""],
    queryFn: () => get<ApprovalRequest>(`/approvals/${id}`),
    enabled: !!id,
    staleTime: staleTimes.config,
  });
}

/** POST /approvals/{id}/approve — X-MFA-Code required; comment required (doc 04 §2.12). */
export function useApprove(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { comment: string; mfaCode: string }) => {
      const req = buildDecisionRequest(args.comment, args.mfaCode);
      return post<ApprovalRequest>(`/approvals/${id}/approve`, req.body, { headers: req.headers });
    },
    onSuccess: (r) => {
      qc.setQueryData(qk.approvals.detail(id), r);
      void qc.invalidateQueries({ queryKey: qk.approvals.root });
    },
  });
}

/** POST /approvals/{id}/reject — same step-up + comment requirement as approve. */
export function useReject(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { comment: string; mfaCode: string }) => {
      const req = buildDecisionRequest(args.comment, args.mfaCode);
      return post<ApprovalRequest>(`/approvals/${id}/reject`, req.body, { headers: req.headers });
    },
    onSuccess: (r) => {
      qc.setQueryData(qk.approvals.detail(id), r);
      void qc.invalidateQueries({ queryKey: qk.approvals.root });
    },
  });
}

/** POST /approvals/{id}/cancel — requester or tenant_admin; terminal (doc 04 §2.12). */
export function useCancel(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => post<ApprovalRequest>(`/approvals/${id}/cancel`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.approvals.root }),
  });
}
