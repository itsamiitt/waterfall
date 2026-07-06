// features/queues — local types from the queue/dead-letter endpoints (doc 04 §2.8) and the
// queue console wireframe (doc 09 §8). Engine-agnostic state vocabulary (QS-TMP-1 hedge).
import type { ErrorClass } from "../../lib/status";

export type QueueState =
  | "waiting"
  | "running"
  | "scheduled"
  | "delayed"
  | "retry"
  | "failed"
  | "dead";

/** Per-state count vector for one queue. */
export type StateVector = Record<QueueState, number>;

export interface QueueSummary extends StateVector {
  name: string;
  oldest_age_s: number;
  /** enqueue / dequeue rates per second (from queue_stats_1m). */
  enq_rate?: number;
  deq_rate?: number;
  /** ACCUMULATING when enq>deq for 5+ buckets (doc 04 §2.8). */
  accumulating?: boolean;
  live_workers?: number;
}

/** GET /queues — the fleet snapshot (also the shape SSE queue.stats.tick replaces). */
export interface QueueListResponse {
  queues: QueueSummary[];
  generated_at?: string;
}

export interface QueueStatsPoint {
  ts: string;
  enq: number;
  deq: number;
  depth: number;
  oldest_age_s: number;
}

export interface QueueStats {
  name: string;
  points: QueueStatsPoint[];
  accumulating?: boolean;
}

export interface JobRow {
  id: string;
  workflow_key: string;
  state: string;
  attempts: number;
  last_error?: string;
  error_class?: ErrorClass | string;
  created_at: string;
}

export interface DeadLetter {
  id: string;
  workflow_key: string;
  attempts: number;
  last_error: string;
  error_class?: ErrorClass | string;
  created_at: string;
  dead: boolean;
}

export interface JobDetail {
  id: string;
  state?: string;
  workflow_key?: string;
  payload: unknown;
  attempts: number;
  last_error?: string;
  dead?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface RedriveResult {
  job_id: string;
  redriven: boolean;
}

/** Replay filter predicate (server re-evaluates under RLS at execution). */
export interface ReplayFilter {
  error_class?: string[];
  before?: string;
  after?: string;
  workflow_key?: string;
}
