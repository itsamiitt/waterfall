// features/workers — local types from the worker endpoints (doc 04 §2.9) and the worker console
// wireframe (doc 09 §9). Desired-state convergence model: the dashboard writes desired_state;
// workers converge via the 10s heartbeat and report status.
import type { WorkerStatus } from "../../lib/status";

export type DesiredState = "running" | "stopped" | "draining" | "paused";

export interface Worker {
  id: string;
  kind: string;
  queue: string;
  region?: string;
  status: WorkerStatus;
  desired_state: DesiredState;
  last_heartbeat_at: string;
  /** Server-derived when present; else computed from last_heartbeat_at. */
  heartbeat_age_s?: number;
  /** Seconds the worker has been diverging from desired_state (for the >5m escalation). */
  converging_for_s?: number;
  jobs_active: number;
  cpu_pct?: number;
  mem_mb?: number;
  converging?: boolean;
}

export interface WorkerListResponse {
  workers: Worker[];
}

export interface WorkerFilters {
  kind?: string;
  queue?: string;
  region?: string;
  status?: string;
}

export interface RollingRestartRequest {
  kind?: string;
  queue?: string;
  max_unavailable: number;
}

export interface ScaleRequest {
  kind: string;
  queue: string;
  replicas: number;
}
