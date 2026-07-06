// features/security/api.ts — the ONLY place security endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §14 rows 33-35, 37):
//   UsersPanel     GET/POST /users, PATCH/DELETE /users/{id}, POST /users/{id}/reset-password
//   SessionsPanel  GET /auth/sessions, DELETE /auth/sessions/{id}
//   AuditPanel     GET /audit-log, GET /audit-log/verify, GET /change-history/{kind}/{id}
//   SettingsPage   GET/PUT /ip-allowlists
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { del, get, patch, post, put } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import { listQuery } from "../../lib/cursors";
import type {
  AuditResponse,
  ChangeHistoryResponse,
  IpAllowlistResponse,
  SessionsResponse,
  UserInput,
  UsersResponse,
  VerifyResult,
} from "./types";
import type { AdminUser } from "../../api/types";

export const sk = {
  users: (q: string) => ["users", "list", q] as const,
  sessions: qk.auth.sessions,
  audit: (q: string) => ["audit-log", q] as const,
  verify: ["audit-log", "verify"] as const,
  changeHistory: (kind: string, id: string) => ["change-history", kind, id] as const,
  ipAllowlists: ["ip-allowlists"] as const,
};

// ---- users ----

export interface UserFilters {
  role?: string;
  status?: string;
  email?: string;
}

export function useUsers(filters: UserFilters) {
  const query = listQuery({ ...filters });
  return useQuery({
    queryKey: sk.users(query),
    queryFn: () => get<UsersResponse>(`/users${query}`),
    staleTime: staleTimes.config,
  });
}

export function useCreateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UserInput) => post<AdminUser>("/users", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.users.root }),
  });
}

export function useUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: string; body: Partial<UserInput> & { status?: string } }) =>
      patch<AdminUser>(`/users/${args.id}`, args.body),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.users.root }),
  });
}

export function useDeleteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => del<void>(`/users/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.users.root }),
  });
}

export function useResetPassword() {
  return useMutation({
    mutationFn: (id: string) => post<void>(`/users/${id}/reset-password`),
  });
}

// ---- sessions ----

export function useSessions() {
  return useQuery({
    queryKey: sk.sessions,
    queryFn: () => get<SessionsResponse>("/auth/sessions"),
    staleTime: staleTimes.config,
  });
}

export function useRevokeSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => del<void>(`/auth/sessions/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.auth.sessions }),
  });
}

// ---- audit ----

export interface AuditFilters {
  actor_user_id?: string;
  action?: string;
  object_kind?: string;
  object_id?: string;
}

export function useAuditLog(filters: AuditFilters) {
  const query = listQuery({ ...filters });
  return useQuery({
    queryKey: sk.audit(query),
    queryFn: () => get<AuditResponse>(`/audit-log${query}`),
    staleTime: staleTimes.telemetry,
  });
}

/** GET /audit-log/verify — walk + verify the hash chain (doc 04 §2.12). Manual, via a button. */
export function useAuditVerify() {
  return useQuery({
    queryKey: sk.verify,
    queryFn: () => get<VerifyResult>("/audit-log/verify"),
    staleTime: 0,
    enabled: false,
  });
}

export function useChangeHistory(kind: string, id: string, enabled: boolean) {
  return useQuery({
    queryKey: sk.changeHistory(kind, id),
    queryFn: () => get<ChangeHistoryResponse>(`/change-history/${kind}/${id}`),
    enabled,
    staleTime: staleTimes.config,
  });
}

// ---- ip allowlists ----

export function useIpAllowlists() {
  return useQuery({
    queryKey: sk.ipAllowlists,
    queryFn: () => get<IpAllowlistResponse>("/ip-allowlists"),
    staleTime: staleTimes.config,
  });
}

export function useUpdateIpAllowlists() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (entries: string[]) => put<IpAllowlistResponse>("/ip-allowlists", { entries }),
    onSuccess: (data) => qc.setQueryData(sk.ipAllowlists, data),
  });
}
