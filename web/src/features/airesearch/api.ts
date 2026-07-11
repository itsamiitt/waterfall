// features/airesearch/api.ts — the ONLY place research endpoint paths are named (doc 08 §2).
// Screen → endpoint map (docs/research-intelligence/08):
//   ResearchPage list  GET /research/dossiers
//   DossierPage detail GET /research/dossiers/{id}
//   RunsPage list      GET /research/runs
import { useQuery } from "@tanstack/react-query";
import { get } from "../../api/client";
import { staleTimes } from "../../api/keys";
import type { DossierDoc, DossiersResponse, RunsResponse } from "./types";

/** Feature-local query keys (mirrors the api/keys.ts convention without editing shared state). */
export const rk = {
  dossiers: ["research", "dossiers"] as const,
  dossier: (id: string) => ["research", "dossier", id] as const,
  runs: ["research", "runs"] as const,
};

/** GET /research/dossiers — the Tenant's dossier summaries, freshest first. */
export function useDossiers() {
  return useQuery({
    queryKey: rk.dossiers,
    queryFn: () => get<DossiersResponse>("/research/dossiers"),
    staleTime: staleTimes.config,
  });
}

/** GET /research/dossiers/{id} — the full stored Dossier document. */
export function useDossier(id: string) {
  return useQuery({
    queryKey: rk.dossier(id),
    queryFn: () => get<DossierDoc>(`/research/dossiers/${encodeURIComponent(id)}`),
    staleTime: staleTimes.config,
    enabled: id !== "",
  });
}

/** GET /research/runs — the async research run lifecycle rows (newest first). Short stale time since a
 * run's status changes as the worker processes it. */
export function useResearchRuns() {
  return useQuery({
    queryKey: rk.runs,
    queryFn: () => get<RunsResponse>("/research/runs"),
    staleTime: staleTimes.telemetry,
  });
}
