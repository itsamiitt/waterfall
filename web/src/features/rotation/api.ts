// features/rotation/api.ts — the ONLY place Module 4 endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §4.2, no-orphan-UI):
//   PoolsList         GET /key-pools?provider_id=&strategy=      (grouped by ownership)
//   PoolDetail        GET /key-pools/{id}
//   StrategyForm      GET /rotation/strategies, PUT /key-pools/{id}/strategy
//   members editor    PUT /key-pools/{id}/members
//   create / patch    POST /key-pools, PATCH /key-pools/{id}, DELETE /key-pools/{id}
//   SelectionState    GET /key-pools/{id}/selection-state         (diagnostic)
//   SimulatePanel     POST /key-pools/{id}/simulate               (zero egress)
//   RotationView      GET /rotation/strategies, GET/PUT /rotation/triggers
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { del, get, patch, post, put } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import { listQuery } from "../../lib/cursors";
import type {
  KeyPool,
  PoolDetail,
  PoolFilter,
  SelectionState,
  SimulateResult,
  StrategyCatalog,
  Triggers,
} from "./types";

export function usePools(filter: PoolFilter) {
  return useQuery({
    queryKey: [...qk.pools.root, "list", filter] as const,
    queryFn: () => get<{ items: KeyPool[] } | KeyPool[]>(`/key-pools${listQuery({ ...filter })}`),
    staleTime: staleTimes.config,
    select: (d) => (Array.isArray(d) ? d : d.items),
  });
}

export function usePool(id: string) {
  return useQuery({
    queryKey: qk.pools.detail(id),
    enabled: id !== "",
    queryFn: () => get<PoolDetail>(`/key-pools/${encodeURIComponent(id)}`),
    staleTime: staleTimes.config,
  });
}

export function useStrategies() {
  return useQuery({
    queryKey: ["rotation", "strategies"] as const,
    queryFn: () => get<StrategyCatalog>("/rotation/strategies"),
    staleTime: staleTimes.config,
  });
}

export function useCreatePool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<KeyPool>) => post<KeyPool>("/key-pools", body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.pools.root }),
  });
}

export function usePatchPool(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<KeyPool>) => patch<KeyPool>(`/key-pools/${encodeURIComponent(id)}`, body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.pools.root }),
  });
}

export function useDeletePool(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => del<void>(`/key-pools/${encodeURIComponent(id)}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.pools.root }),
  });
}

/** PUT /key-pools/{id}/strategy — bumps the key_pool epoch; PoolState rebuilds ≤1s UNVERIFIED. */
export function usePutStrategy(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { strategy: string; strategy_params: Record<string, unknown> }) =>
      put<KeyPool>(`/key-pools/${encodeURIComponent(id)}/strategy`, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.pools.detail(id) });
      void qc.invalidateQueries({ queryKey: qk.keys.root });
    },
  });
}

export function usePutMembers(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (keyIds: string[]) =>
      put<PoolDetail>(`/key-pools/${encodeURIComponent(id)}/members`, { key_ids: keyIds }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.pools.detail(id) }),
  });
}

/** GET /key-pools/{id}/selection-state — per-instance cache; a 404 means "not resident on this
 * instance" and renders as an info state, not an error (doc 09 §4.3). */
export function useSelectionState(id: string) {
  return useQuery({
    queryKey: [...qk.pools.detail(id), "selection-state"] as const,
    enabled: id !== "",
    queryFn: () => get<SelectionState>(`/key-pools/${encodeURIComponent(id)}/selection-state`),
    staleTime: staleTimes.telemetry,
    retry: false,
  });
}

/** POST /key-pools/{id}/simulate — zero-egress selection distribution (doc 04 §2.5). */
export function useSimulate(id: string) {
  return useMutation({
    mutationFn: (draws: number) =>
      post<SimulateResult>(`/key-pools/${encodeURIComponent(id)}/simulate`, { draws }),
  });
}

export function useTriggers() {
  return useQuery({
    queryKey: ["rotation", "triggers"] as const,
    queryFn: () => get<Triggers>("/rotation/triggers"),
    staleTime: staleTimes.config,
  });
}

export function usePutTriggers() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Triggers) => put<Triggers>("/rotation/triggers", body),
    onSuccess: (data) => qc.setQueryData(["rotation", "triggers"], data),
  });
}
