// features/workers/convergence.ts — PURE desired-vs-actual convergence logic (doc 09 §9),
// unit-tested with no DOM. status and desired_state are two visible columns; when they differ the
// row shows a `converging` badge with elapsed time. `lost` (heartbeat age > 3×10s) renders the
// error token. Non-convergence is NOT an error — but converging > 5 min escalates to warn with a
// runbook pointer (doc 09 §9.3).
import { workerStatusInfo, type IconName, type StatusToken, type WorkerStatus } from "../../lib/status";
import type { DesiredState, Worker } from "./types";

/** Heartbeat age (s) beyond which the server derives `lost` (3 × 10s interval, doc 04 §2.9). */
export const LOST_HEARTBEAT_S = 30;
/** Converging longer than this escalates the badge to warn (doc 09 §9.3). */
export const CONVERGENCE_WARN_S = 300;

/** The target status a desired_state converges toward. */
export function desiredStatus(d: DesiredState): WorkerStatus {
  switch (d) {
    case "running":
      return "running";
    case "paused":
      return "paused";
    case "stopped":
      return "stopped";
    case "draining":
      return "draining";
  }
}

export interface ConvergenceBadge {
  converging: boolean;
  /** True once convergence has stalled past CONVERGENCE_WARN_S (warn, not error). */
  escalated: boolean;
  token: StatusToken;
  icon: IconName;
  label: string;
}

/**
 * The badge shown when status ≠ desired_state (doc 09 §9). `lost` short-circuits to error; a
 * matched pair shows the plain status; a mismatch shows converging (info), escalating to warn if
 * it has been diverging past 5 minutes.
 */
export function workerConvergence(w: Pick<Worker, "status" | "desired_state" | "converging_for_s" | "converging">): ConvergenceBadge {
  if (w.status === "lost") {
    const info = workerStatusInfo("lost");
    return { converging: false, escalated: true, token: info.token, icon: info.icon, label: info.label };
  }

  const target = desiredStatus(w.desired_state);
  const diverged = w.status !== target || w.converging === true;
  if (!diverged) {
    const info = workerStatusInfo(w.status);
    return { converging: false, escalated: false, token: info.token, icon: info.icon, label: info.label };
  }

  const stalled = (w.converging_for_s ?? 0) > CONVERGENCE_WARN_S;
  return stalled
    ? { converging: true, escalated: true, token: "warn", icon: "triangle", label: "Converging (stalled)" }
    : { converging: true, escalated: false, token: "info", icon: "clock", label: "Converging" };
}

/** True when the worker's heartbeat is older than the lost threshold (belt-and-suspenders). */
export function isHeartbeatStale(w: Pick<Worker, "heartbeat_age_s">): boolean {
  return (w.heartbeat_age_s ?? 0) > LOST_HEARTBEAT_S;
}
