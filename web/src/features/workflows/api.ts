// features/workflows/api.ts — the ONLY place workflow endpoint paths are named (doc 08 §2).
// Screen → endpoint map (no-orphan-UI, doc 09 §14):
//   WorkflowListPage   GET  /workflows?trigger=&q=
//   VersionRail        GET  /workflows/{scope}/versions
//   Editor load        GET  /workflows/{scope}/versions/{id}
//   Create draft       POST /workflows/{scope}/versions
//   Canvas edit        PATCH /workflows/{scope}/versions/{id}
//   Validate           POST /workflows/{scope}/versions/{id}/validate
//   Dry-run panel      POST /workflows/{scope}/versions/{id}/dry-run
//   Publish            POST /workflows/{scope}/versions/{id}/publish
//   Rollback           POST /workflows/{scope}/rollback
//   Clone              POST /workflows/{scope}/versions/{id}/clone
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { get, patch, post } from "../../api/client";
import { staleTimes } from "../../api/keys";
import type {
  ConfigVersion,
  DryRunResult,
  PublishResponse,
  ValidationReport,
  WaterfallWorkflowPayload,
  WorkflowIndexItem,
} from "./types";

const seg = (scope: string) => encodeURIComponent(scope);

export const wk = {
  root: ["workflows"] as const,
  index: (filters?: Record<string, unknown>) => ["workflows", "index", filters ?? {}] as const,
  versions: (scope: string) => ["workflows", "versions", scope] as const,
  version: (scope: string, id: string) => ["workflows", "version", scope, id] as const,
};

export function useWorkflowIndex(filters?: { trigger?: string; q?: string }) {
  const params = new URLSearchParams();
  if (filters?.trigger) params.set("trigger", filters.trigger);
  if (filters?.q) params.set("q", filters.q);
  const qs = params.toString();
  return useQuery({
    queryKey: wk.index(filters),
    queryFn: () => get<{ workflows: WorkflowIndexItem[] }>(`/workflows${qs ? `?${qs}` : ""}`),
    staleTime: staleTimes.config,
  });
}

export function useWorkflowVersions(scope: string) {
  return useQuery({
    queryKey: wk.versions(scope),
    queryFn: () => get<{ versions: ConfigVersion[] }>(`/workflows/${seg(scope)}/versions`),
    staleTime: staleTimes.config,
  });
}

export function useWorkflowVersion(scope: string, id: string | undefined) {
  return useQuery({
    queryKey: wk.version(scope, id ?? ""),
    queryFn: () => get<ConfigVersion>(`/workflows/${seg(scope)}/versions/${id}`),
    staleTime: staleTimes.config,
    enabled: !!id,
  });
}

function invalidateScope(qc: QueryClient, scope: string) {
  void qc.invalidateQueries({ queryKey: wk.versions(scope) });
  void qc.invalidateQueries({ queryKey: wk.root });
}

export function useCreateDraft(scope: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (payload: WaterfallWorkflowPayload) =>
      post<ConfigVersion>(`/workflows/${seg(scope)}/versions`, { payload }),
    onSuccess: () => invalidateScope(qc, scope),
  });
}

export function usePatchDraft(scope: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (payload: WaterfallWorkflowPayload) =>
      patch<ConfigVersion>(`/workflows/${seg(scope)}/versions/${id}`, { payload }),
    onSuccess: (v) => {
      qc.setQueryData(wk.version(scope, id), v);
      void qc.invalidateQueries({ queryKey: wk.versions(scope) });
    },
  });
}

export function useValidate(scope: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      post<ConfigVersion & { validation_report: ValidationReport }>(
        `/workflows/${seg(scope)}/versions/${id}/validate`,
      ),
    onSuccess: (v) => {
      qc.setQueryData(wk.version(scope, id), v);
      void qc.invalidateQueries({ queryKey: wk.versions(scope) });
    },
  });
}

export function useDryRun(scope: string, id: string) {
  return useMutation({
    mutationFn: (sample?: Record<string, unknown>) =>
      post<DryRunResult>(`/workflows/${seg(scope)}/versions/${id}/dry-run`, sample ? { sample } : undefined),
  });
}

export function usePublish(scope: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (expectedActiveVersionId?: string | null) =>
      post<PublishResponse>(
        `/workflows/${seg(scope)}/versions/${id}/publish`,
        expectedActiveVersionId ? { expected_active_version_id: expectedActiveVersionId } : undefined,
      ),
    onSuccess: () => invalidateScope(qc, scope),
  });
}

export function useRollback(scope: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (toVersion: number) =>
      post<PublishResponse>(`/workflows/${seg(scope)}/rollback`, { to_version: toVersion }),
    onSuccess: () => invalidateScope(qc, scope),
  });
}

export function useClone(scope: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => post<ConfigVersion>(`/workflows/${seg(scope)}/versions/${id}/clone`),
    onSuccess: () => invalidateScope(qc, scope),
  });
}
