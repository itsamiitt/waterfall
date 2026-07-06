// features/routing/api.ts — the ONLY place routing endpoint paths are named (doc 08 §2).
// Screen → endpoint map (no-orphan-UI, doc 09 §14):
//   RoutingListPage      GET  /routing
//   VersionRail          GET  /routing/{scope}/versions
//   RoutingEditor load   GET  /routing/{scope}/versions/{id}
//   Create draft         POST /routing/{scope}/versions
//   Edit (debounced)     PATCH /routing/{scope}/versions/{id}
//   Validate button      POST /routing/{scope}/versions/{id}/validate
//   Dry-run panel        POST /routing/{scope}/versions/{id}/dry-run
//   Publish button       POST /routing/{scope}/versions/{id}/publish  (202 approval | 200)
//   Version rail rollback POST /routing/{scope}/rollback
//   Clone                POST /routing/{scope}/versions/{id}/clone
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { get, post, patch } from "../../api/client";
import { staleTimes } from "../../api/keys";
import type {
  ConfigVersion,
  DryRunResult,
  PublishResponse,
  RoutingPolicyPayload,
  RoutingScopeList,
  ValidationReport,
} from "./types";

const seg = (scope: string) => encodeURIComponent(scope);

/** Feature-local query keys (mirrors the api/keys.ts convention without editing shared state). */
export const rk = {
  root: ["routing"] as const,
  scopes: ["routing", "scopes"] as const,
  versions: (scope: string) => ["routing", "versions", scope] as const,
  version: (scope: string, id: string) => ["routing", "version", scope, id] as const,
};

export function useRoutingScopes() {
  return useQuery({
    queryKey: rk.scopes,
    queryFn: () => get<RoutingScopeList>("/routing"),
    staleTime: staleTimes.config,
  });
}

export function useRoutingVersions(scope: string) {
  return useQuery({
    queryKey: rk.versions(scope),
    queryFn: () => get<{ versions: ConfigVersion[] }>(`/routing/${seg(scope)}/versions`),
    staleTime: staleTimes.config,
  });
}

export function useRoutingVersion(scope: string, id: string | undefined) {
  return useQuery({
    queryKey: rk.version(scope, id ?? ""),
    queryFn: () => get<ConfigVersion>(`/routing/${seg(scope)}/versions/${id}`),
    staleTime: staleTimes.config,
    enabled: !!id,
  });
}

function invalidateScope(qc: QueryClient, scope: string) {
  void qc.invalidateQueries({ queryKey: rk.versions(scope) });
  void qc.invalidateQueries({ queryKey: rk.scopes });
}

export function useCreateDraft(scope: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (payload: RoutingPolicyPayload) =>
      post<ConfigVersion>(`/routing/${seg(scope)}/versions`, { payload }),
    onSuccess: () => invalidateScope(qc, scope),
  });
}

export function usePatchDraft(scope: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (payload: RoutingPolicyPayload) =>
      patch<ConfigVersion>(`/routing/${seg(scope)}/versions/${id}`, { payload }),
    onSuccess: (v) => {
      qc.setQueryData(rk.version(scope, id), v);
      void qc.invalidateQueries({ queryKey: rk.versions(scope) });
    },
  });
}

export function useValidate(scope: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      post<ConfigVersion & { validation_report: ValidationReport }>(
        `/routing/${seg(scope)}/versions/${id}/validate`,
      ),
    onSuccess: (v) => {
      qc.setQueryData(rk.version(scope, id), v);
      void qc.invalidateQueries({ queryKey: rk.versions(scope) });
    },
  });
}

export function useDryRun(scope: string, id: string) {
  return useMutation({
    mutationFn: (sample?: Record<string, unknown>) =>
      post<DryRunResult>(`/routing/${seg(scope)}/versions/${id}/dry-run`, sample ? { sample } : undefined),
  });
}

export function usePublish(scope: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    // expected_active_version_id defaults server-side to the draft's parent_version_id (doc 04 §2.7).
    mutationFn: (expectedActiveVersionId?: string | null) =>
      post<PublishResponse>(
        `/routing/${seg(scope)}/versions/${id}/publish`,
        expectedActiveVersionId ? { expected_active_version_id: expectedActiveVersionId } : undefined,
      ),
    onSuccess: () => invalidateScope(qc, scope),
  });
}

export function useRollback(scope: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (toVersion: number) =>
      post<PublishResponse>(`/routing/${seg(scope)}/rollback`, { to_version: toVersion }),
    onSuccess: () => invalidateScope(qc, scope),
  });
}

export function useClone(scope: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => post<ConfigVersion>(`/routing/${seg(scope)}/versions/${id}/clone`),
    onSuccess: () => invalidateScope(qc, scope),
  });
}
