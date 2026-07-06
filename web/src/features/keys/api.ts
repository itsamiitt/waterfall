// features/keys/api.ts — the ONLY place Module 3 endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §3.3, no-orphan-UI):
//   KeyGrid            GET /providers/{id}/keys?…      (useInfiniteQuery, virtualized, cursors)
//   match count        POST /keys/bulk {preview:true}  (aria-rowcount + "select all N")
//   KeyDrawer          GET /keys/{id}, GET /keys/{id}/usage
//   row actions        POST /keys/{id}/{enable|disable|test|health-check|refresh-credits}
//   rotate             POST /keys/{id}/rotate          (X-MFA-Code, doc 05 §5.4)
//   edit / archive     PATCH /keys/{id}, DELETE /keys/{id}
//   add key            POST /providers/{id}/keys       (X-MFA-Code)
//   bulk bar           POST /keys/bulk                 (job or approval; delete → 202 approval)
//   bulk progress      GET /bulk-jobs/{id} + SSE import
//   import wizard      POST /providers/{id}/keys/import (X-MFA-Code) → 202 {job_id}
//   import progress    GET /key-imports/{job_id} + SSE import
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { del, get, patch, post } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import { getNextPageParam, initialPageParam, listQuery, type Page } from "../../lib/cursors";
import type { TimeRange } from "../../design/primitives";
import type { Accepted, JobAccepted } from "../../api/types";
import { buildPreviewRequest } from "./bulkFilter";
import type {
  BulkRequest,
  JobProgress,
  KeyFilter,
  PreviewCount,
  ProviderKey,
  RotateResult,
} from "./types";

const PAGE_SIZE = 50;

function keyListQuery(filter: KeyFilter, sort: string, cursor?: string): string {
  return listQuery(
    {
      status: filter.status,
      health: filter.health,
      region: filter.region,
      environment: filter.environment,
      tag: filter.tag,
      rotation_group: filter.rotation_group,
      imported_batch_id: filter.imported_batch_id,
      pool_id: filter.pool_id,
      sort: sort || undefined,
    },
    { limit: PAGE_SIZE, cursor },
  );
}

/** GET /providers/{id}/keys — cursor-paginated; the grid fetches ahead on scroll (doc 04 §2.4). */
export function useProviderKeys(providerId: string, filter: KeyFilter, sort: string) {
  return useInfiniteQuery({
    queryKey: [...qk.providers.keys(providerId), filter, sort] as const,
    enabled: providerId !== "",
    queryFn: ({ pageParam }) =>
      get<Page<ProviderKey>>(
        `/providers/${encodeURIComponent(providerId)}/keys${keyListQuery(filter, sort, pageParam)}`,
      ),
    initialPageParam,
    getNextPageParam,
    staleTime: staleTimes.config,
  });
}

/** POST /keys/bulk preview → total matching the active filter; backs aria-rowcount and the
 * "select all N matching filter" escalation (doc 04 §4.2; nothing is executed under preview). */
export function useKeyMatchCount(providerId: string, filter: KeyFilter) {
  return useQuery({
    queryKey: [...qk.providers.keys(providerId), "count", filter] as const,
    enabled: providerId !== "",
    queryFn: () => post<PreviewCount>("/keys/bulk", buildPreviewRequest(providerId, filter)),
    staleTime: staleTimes.telemetry,
  });
}

export function useKey(id: string) {
  return useQuery({
    queryKey: qk.keys.detail(id),
    queryFn: () => get<ProviderKey>(`/keys/${encodeURIComponent(id)}`),
    staleTime: staleTimes.config,
  });
}

export function useKeyUsage(id: string, range: TimeRange) {
  return useQuery({
    queryKey: [...qk.keys.detail(id), "usage", range.from, range.to] as const,
    queryFn: () =>
      get<{ points: { ts: string; calls?: number; successes?: number }[] }>(
        `/keys/${encodeURIComponent(id)}/usage${listQuery({ res: range.res, from: range.from, to: range.to })}`,
      ),
    staleTime: staleTimes.telemetry,
  });
}

export type KeyAction = "enable" | "disable" | "test" | "health-check" | "refresh-credits";

export function useKeyAction(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (action: KeyAction) => post<ProviderKey>(`/keys/${encodeURIComponent(id)}/${action}`, {}),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.keys.root }),
  });
}

export function useRotateKey(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { secret: string; overlap_s: number; mfaCode?: string }) =>
      post<RotateResult>(
        `/keys/${encodeURIComponent(id)}/rotate`,
        { secret: vars.secret, overlap_s: vars.overlap_s },
        vars.mfaCode ? { headers: { "X-MFA-Code": vars.mfaCode } } : undefined,
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.keys.root }),
  });
}

export function usePatchKey(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<ProviderKey>) => patch<ProviderKey>(`/keys/${encodeURIComponent(id)}`, body),
    onSuccess: (data) => {
      qc.setQueryData(qk.keys.detail(id), data);
      void qc.invalidateQueries({ queryKey: qk.keys.root });
    },
  });
}

export function useArchiveKey(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => del<void>(`/keys/${encodeURIComponent(id)}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.keys.root }),
  });
}

export function useCreateKey(providerId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { body: Partial<ProviderKey> & { secret: string }; mfaCode?: string }) =>
      post<ProviderKey>(
        `/providers/${encodeURIComponent(providerId)}/keys`,
        vars.body,
        vars.mfaCode ? { headers: { "X-MFA-Code": vars.mfaCode } } : undefined,
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.keys.root }),
  });
}

/** POST /keys/bulk — 202 {job_id}; op=delete → 202 {approval_request_id} (doc 04 §2.4/§4). */
export function useBulk() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: BulkRequest) => post<Accepted>("/keys/bulk", req),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.keys.root }),
  });
}

/** POST /providers/{id}/keys/import — canonical paste payload (client applies the column map);
 * 202 {job_id} where job_id IS key_import_batches.id (doc 04 §2.4). */
export function useImportKeys(providerId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { data: string; mfaCode?: string }) =>
      post<JobAccepted>(
        `/providers/${encodeURIComponent(providerId)}/keys/import`,
        { format: "paste", data: vars.data },
        vars.mfaCode ? { headers: { "X-MFA-Code": vars.mfaCode } } : undefined,
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.imports.root }),
  });
}

/** GET /key-imports/{job_id} — import batch progress; live via SSE topic `import`. */
export function useImportProgress(jobId: string | null) {
  return useQuery({
    queryKey: qk.imports.detail(jobId ?? ""),
    enabled: jobId !== null,
    queryFn: () => get<JobProgress>(`/key-imports/${encodeURIComponent(jobId!)}`),
    staleTime: staleTimes.telemetry,
  });
}

/** GET /bulk-jobs/{id} — bulk job progress; live via SSE topic `import` (shared drawer). */
export function useBulkProgress(jobId: string | null) {
  return useQuery({
    queryKey: [...qk.imports.root, "bulk", jobId ?? ""] as const,
    enabled: jobId !== null,
    queryFn: () => get<JobProgress>(`/bulk-jobs/${encodeURIComponent(jobId!)}`),
    staleTime: staleTimes.telemetry,
  });
}

/** POST /bulk-jobs/{id}/cancel — request cooperative cancellation of an in-flight bulk/import job
 * (doc 15 §T3). The executor drives it to the terminal `cancelled` status; already-committed rows
 * are retained (G2-idempotent). No step-up (parity with POST /keys/bulk). Invalidating the imports
 * root refetches both the /bulk-jobs and /key-imports progress views so the status flips live. */
export function useCancelBulkJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (jobId: string) => post<JobProgress>(`/bulk-jobs/${encodeURIComponent(jobId)}/cancel`, {}),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.imports.root }),
  });
}
