// features/workers/api.ts — the ONLY place worker endpoint paths are named (doc 08 §2). Keys are
// nested under qk.workers.root so worker.state.changed invalidation refetches the fleet.
// Screen → endpoint map (no-orphan-UI, doc 09 §14):
//   WorkersPage grid    GET  /workers?kind=&queue=&region=&status=  (live: worker.state.changed)
//   Drain button        POST /workers/{id}/drain
//   Restart/Pause/Resume POST /workers/{id}/restart|pause|resume
//   Scale intent        POST /workers/scale
//   Rolling restart     POST /workers/rolling-restart   (202 job)
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import type { JobAccepted } from "../../api/types";
import type {
  RollingRestartRequest,
  ScaleRequest,
  Worker,
  WorkerFilters,
  WorkerListResponse,
} from "./types";

export function useWorkers(filters: WorkerFilters) {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) if (v) params.set(k, v);
  const qs = params.toString();
  return useQuery({
    queryKey: [...qk.workers.root, "list", filters] as const,
    queryFn: () => get<WorkerListResponse>(`/workers${qs ? `?${qs}` : ""}`),
    staleTime: staleTimes.telemetry,
  });
}

type WorkerAction = "drain" | "restart" | "pause" | "resume";

export function useWorkerAction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, action }: { id: string; action: WorkerAction }) =>
      post<Worker>(`/workers/${encodeURIComponent(id)}/${action}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.workers.root }),
  });
}

export function useScale() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: ScaleRequest) => post<{ recorded: boolean }>(`/workers/scale`, req),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.workers.root }),
  });
}

export function useRollingRestart() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: RollingRestartRequest) => post<JobAccepted>(`/workers/rolling-restart`, req),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.workers.root }),
  });
}
