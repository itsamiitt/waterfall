// features/queues/api.ts — the ONLY place queue endpoint paths are named (doc 08 §2). Keys are
// sourced from api/keys.ts (qk.queues.*) so the SSE manager's queue.stats.tick replace-snapshot
// (which setQueryData's qk.queues.snapshot) and queue-topic invalidation both land here.
// Screen → endpoint map (no-orphan-UI, doc 09 §14):
//   QueuesPage cards     GET  /queues                      (live: queue.stats.tick)
//   QueueConsole stats   GET  /queues/{name}/stats
//   Job table by state   GET  /queues/{name}/jobs?state=
//   DeadLettersPage      GET  /dead-letters
//   DeadLetterDrawer     GET  /jobs/{id}
//   Redrive button       POST /dead-letters/{id}/redrive
//   Replay all matching  POST /queues/{name}/replay        (202 job)
//   Desired workers      PUT  /queues/{name}/workers
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post, put } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import type {
  DeadLetter,
  JobDetail,
  JobRow,
  QueueListResponse,
  QueueStats,
  RedriveResult,
  ReplayFilter,
} from "./types";
import type { JobAccepted } from "../../api/types";

/** Feature-local keys nested under qk.queues.root so queue-topic invalidation refetches them. */
export const localKeys = {
  jobs: (name: string, state: string) => ["queues", "jobs", name, state] as const,
  deadLetters: (filters: Record<string, string>) => ["queues", "dead-letters", filters] as const,
  job: (id: string) => ["queues", "job", id] as const,
};

export function useQueues() {
  return useQuery({
    queryKey: qk.queues.snapshot,
    queryFn: () => get<QueueListResponse>("/queues"),
    staleTime: staleTimes.telemetry,
  });
}

export function useQueueStats(name: string, win: string) {
  return useQuery({
    queryKey: qk.queues.stats(name, win),
    queryFn: () => get<QueueStats>(`/queues/${encodeURIComponent(name)}/stats?res=1m`),
    staleTime: staleTimes.telemetry,
  });
}

export function useQueueJobs(name: string, state: string) {
  return useQuery({
    queryKey: localKeys.jobs(name, state),
    queryFn: () =>
      get<{ jobs: JobRow[] }>(`/queues/${encodeURIComponent(name)}/jobs?state=${encodeURIComponent(state)}`),
    staleTime: staleTimes.telemetry,
    enabled: !!state,
  });
}

export function useDeadLetters(filters: { error_class?: string; before?: string; after?: string }) {
  const params = new URLSearchParams();
  if (filters.error_class) params.set("error_class", filters.error_class);
  if (filters.before) params.set("before", filters.before);
  if (filters.after) params.set("after", filters.after);
  const qs = params.toString();
  return useQuery({
    queryKey: localKeys.deadLetters({ ...filters } as Record<string, string>),
    queryFn: () => get<{ dead_letters: DeadLetter[] }>(`/dead-letters${qs ? `?${qs}` : ""}`),
    staleTime: staleTimes.telemetry,
  });
}

export function useJobDetail(id: string | undefined) {
  return useQuery({
    queryKey: localKeys.job(id ?? ""),
    queryFn: () => get<JobDetail>(`/jobs/${id}`),
    staleTime: staleTimes.telemetry,
    enabled: !!id,
  });
}

export function useRedrive() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => post<RedriveResult>(`/dead-letters/${id}/redrive`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.queues.root }),
  });
}

export function useReplay(name: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (filter: ReplayFilter) =>
      post<JobAccepted>(`/queues/${encodeURIComponent(name)}/replay`, { filter }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.queues.root }),
  });
}

export function useSetDesiredWorkers(name: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (replicas: number) =>
      put<{ name: string; desired: number }>(`/queues/${encodeURIComponent(name)}/workers`, { replicas }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.queues.root }),
  });
}
