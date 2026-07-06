// features/queues/redrive.ts — PURE copy + helpers for the dead-letter redrive confirm (doc 09
// §8.1). Redrive is a single UPDATE guarded WHERE dead=true (idempotent by construction); HTTP
// retry is covered by the G2 Idempotency-Key ledger. This module owns the explainer copy so it
// is unit-testable (the P10 "DLQ redrive confirm" gate).
import type { DeadLetter } from "./types";

/** The G2-idempotency explainer shown in the redrive ConfirmDialog (doc 09 §8.1, verbatim intent). */
export const REDRIVE_EXPLAINER =
  "Redrive resets attempts to 0 and re-delivers at-least-once. Re-execution is safe: the engine's " +
  "G2 Idempotency Key ledger makes Provider calls exactly-once-effective. Double-click is a no-op " +
  "(dead=true guard).";

/** Concrete consequence bullets for the ConfirmDialog (doc 08 §6.2: destructive ops name effects). */
export function redriveConsequences(job: DeadLetter): string[] {
  return [
    `Job ${job.id} · ${job.workflow_key}`,
    `Resets attempts (${job.attempts} → 0) and re-delivers at-least-once`,
    "Provider calls stay exactly-once-effective via the G2 Idempotency-Key ledger",
    "Double-click is a no-op — the WHERE dead=true guard already cleared it",
  ];
}

/** Copy for the "already redriven or gone" outcome (404 not_found → info, not error; doc 09 §8.3). */
export const REDRIVE_ALREADY_GONE = "Already redriven or gone — the row was cleared.";

/** Human summary for a filtered replay ConfirmDialog. */
export function replaySummary(filter: {
  error_class?: string[];
  before?: string;
  after?: string;
  workflow_key?: string;
}): string {
  const parts: string[] = [];
  if (filter.error_class?.length) parts.push(`error_class ∈ {${filter.error_class.join(", ")}}`);
  if (filter.workflow_key) parts.push(`workflow ${filter.workflow_key}`);
  if (filter.after) parts.push(`after ${filter.after}`);
  if (filter.before) parts.push(`before ${filter.before}`);
  return parts.length ? parts.join(" · ") : "all parked jobs on this queue";
}
