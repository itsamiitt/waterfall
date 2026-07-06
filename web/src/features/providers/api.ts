// features/providers/api.ts — the ONLY place Module 2 endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §2.2, no-orphan-UI):
//   ProvidersList        GET /providers                          (list/filter/sort, cursors)
//   ProviderDetail       GET /providers/{id}                     (header + config tab)
//   ProviderConfigForm   GET /meta/enums, PATCH /providers/{id}  (closed-vocab selects; save)
//   ConfigForm create    POST /providers                          (201 DEPRIORITIZED)
//   Lifecycle actions    POST /providers/{id}/{enable|disable|pause|maintenance|test|
//                             health-check|refresh-metadata|sync-credits|benchmark|duplicate|archive}
//   Delete               DELETE /providers/{id}                   (approval-gated → 202)
//   Health tab           GET /providers/{id}/health
//   Stats tab            GET /providers/{id}/stats?res=&from=&to=
//   History tab          GET /change-history/provider/{id}
//   Compare              GET /providers/compare?ids= , /coverage , /rankings
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
import type {
  ChangeHistoryEvent,
  CompareResult,
  MetaEnums,
  Provider,
  ProviderFilter,
  ProviderHealth,
  ProviderStats,
  RankingRow,
} from "./types";
import type { Accepted } from "../../api/types";

const PAGE_SIZE = 50;

/** GET /providers — cursor-paginated catalog; server-side sort/filter (doc 04 §2.3). */
export function useProviders(filter: ProviderFilter, sort: string) {
  const params = { ...filter, sort: sort || undefined };
  return useInfiniteQuery({
    queryKey: qk.providers.list({ ...params }),
    queryFn: ({ pageParam }) =>
      get<Page<Provider>>(`/providers${listQuery(params, { limit: PAGE_SIZE, cursor: pageParam })}`),
    initialPageParam,
    getNextPageParam,
    staleTime: staleTimes.config,
  });
}

/** GET /providers/{id} — full detail (doc 04 §2.3). */
export function useProvider(id: string) {
  return useQuery({
    queryKey: qk.providers.detail(id),
    queryFn: () => get<Provider>(`/providers/${encodeURIComponent(id)}`),
    staleTime: staleTimes.config,
  });
}

/** GET /meta/enums — closed vocabularies that drive the config form's selects (doc 04 §2.13). */
export function useMetaEnums() {
  return useQuery({
    queryKey: ["meta", "enums"] as const,
    queryFn: () => get<MetaEnums>("/meta/enums"),
    staleTime: staleTimes.config,
  });
}

export function useProviderHealth(id: string) {
  return useQuery({
    queryKey: qk.providers.health(id),
    queryFn: () => get<ProviderHealth>(`/providers/${encodeURIComponent(id)}/health`),
    staleTime: staleTimes.telemetry,
  });
}

export function useProviderStats(id: string, range: TimeRange) {
  return useQuery({
    queryKey: qk.providers.stats(id, `${range.from}:${range.to}:${range.res}`),
    queryFn: () =>
      get<ProviderStats>(
        `/providers/${encodeURIComponent(id)}/stats${listQuery({
          res: range.res,
          from: range.from,
          to: range.to,
        })}`,
      ),
    staleTime: staleTimes.telemetry,
  });
}

export function useProviderHistory(id: string) {
  return useQuery({
    queryKey: [...qk.providers.detail(id), "history"] as const,
    queryFn: () =>
      get<Page<ChangeHistoryEvent>>(`/change-history/provider/${encodeURIComponent(id)}`),
    staleTime: staleTimes.config,
  });
}

export function useCompare(ids: string[]) {
  return useQuery({
    queryKey: ["providers", "compare", ids.slice().sort().join(",")] as const,
    enabled: ids.length > 0,
    queryFn: () => get<CompareResult>(`/providers/compare${listQuery({ ids })}`),
    staleTime: staleTimes.config,
  });
}

export function useRankings() {
  return useQuery({
    queryKey: ["providers", "rankings"] as const,
    queryFn: () => get<Page<RankingRow>>("/providers/rankings"),
    staleTime: staleTimes.config,
  });
}

/** The closed set of ungated lifecycle POST actions (doc 04 §2.3). */
export type ProviderAction =
  | "enable"
  | "disable"
  | "pause"
  | "maintenance"
  | "test"
  | "health-check"
  | "refresh-metadata"
  | "sync-credits"
  | "benchmark"
  | "duplicate"
  | "archive";

/** POST /providers/{id}/{action}. archive/delete are approval-gated → 202 {approval_request_id};
 * benchmark → 202 {job_id}; the rest return the updated Provider. Discriminate on the field. */
export function useProviderAction(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (action: ProviderAction) =>
      post<Provider | Accepted>(`/providers/${encodeURIComponent(id)}/${action}`, {}),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.providers.root });
    },
  });
}

/** DELETE /providers/{id} — approval-gated (doc 04 §2.3) → 202 {approval_request_id}. */
export function useDeleteProvider(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => del<Accepted>(`/providers/${encodeURIComponent(id)}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.providers.root });
    },
  });
}

/** PATCH /providers/{id} — partial catalog/ops update (doc 04 §2.3). */
export function useUpdateProvider(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<Provider>) =>
      patch<Provider>(`/providers/${encodeURIComponent(id)}`, body),
    onSuccess: (data) => {
      qc.setQueryData(qk.providers.detail(id), data);
      void qc.invalidateQueries({ queryKey: qk.providers.root });
    },
  });
}

/** POST /providers — create (doc 04 §2.3); lands DEPRIORITIZED pending compliance review. */
export function useCreateProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<Provider>) => post<Provider>("/providers", body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.providers.root });
    },
  });
}
