// features/health/api.ts — the ONLY place Module 5 endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §5.2, no-orphan-UI):
//   FleetHealth       GET /health/providers?status=&region=   (worst-first)
//   ProviderTimeline  GET /health/providers/{id}/timeline      (90 day + 48h buckets)
//   P95/P99 overlay   GET /providers/{id}/stats?res=&from=&to= (computed at read from lat_hist)
//   RegionalView      GET /health/regional
//   Schedules         GET /health/schedules , PUT /health/schedules
//   Run checks now    POST /health/checks/run  (≤5 inline 200, else 202 {job_id})
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post, put } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import { listQuery } from "../../lib/cursors";
import type { Accepted } from "../../api/types";
import type { HealthFilter, HealthSchedules, HealthTimeline, RegionalMatrix } from "./types";
import type { HealthRow } from "./types";

export function useFleetHealth(filter: HealthFilter) {
  return useQuery({
    queryKey: [...qk.health.root, "fleet", filter] as const,
    queryFn: () =>
      get<{ items: HealthRow[] } | HealthRow[]>(`/health/providers${listQuery({ ...filter })}`),
    staleTime: staleTimes.telemetry,
    select: (d) => (Array.isArray(d) ? d : d.items),
  });
}

export function useTimeline(providerId: string) {
  return useQuery({
    queryKey: [...qk.health.root, "timeline", providerId] as const,
    enabled: providerId !== "",
    queryFn: () => get<HealthTimeline>(`/health/providers/${encodeURIComponent(providerId)}/timeline`),
    staleTime: staleTimes.telemetry,
  });
}

export function useRegional() {
  return useQuery({
    queryKey: [...qk.health.root, "regional"] as const,
    queryFn: () => get<RegionalMatrix>("/health/regional"),
    staleTime: staleTimes.telemetry,
  });
}

export function useSchedules() {
  return useQuery({
    queryKey: [...qk.health.root, "schedules"] as const,
    queryFn: () => get<HealthSchedules>("/health/schedules"),
    staleTime: staleTimes.config,
  });
}

export function usePutSchedules() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: HealthSchedules) => put<HealthSchedules>("/health/schedules", body),
    onSuccess: (data) => qc.setQueryData([...qk.health.root, "schedules"], data),
  });
}

/** POST /health/checks/run — 200 inline for ≤5 providers, else 202 {job_id} → progress drawer. */
export function useRunChecks() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (providerIds: string[]) =>
      post<Accepted | { ran: number }>("/health/checks/run", { provider_ids: providerIds }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.health.root }),
  });
}
