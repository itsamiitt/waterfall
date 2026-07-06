// features/keys/bulkFilter.ts — the escalation core of the bulk bar (doc 09 §3.1, P9
// acceptance #2). Page checkboxes select ids; escalating to "all M matching filter" sends the
// FILTER PREDICATE, never an id list — the server re-evaluates it under RLS at execution and
// reports matched_at_execution (TOCTOU documented, doc 04 §4.2). Pure + unit-tested.
import type { BulkOp, BulkRequest, KeyFilter } from "./types";

/** Two mutually-exclusive scopes (doc 04 §4.2: ids OR filter, never both). */
export type BulkScope =
  | { mode: "ids"; ids: readonly string[] }
  | { mode: "filter" };

/** Assemble the POST /keys/bulk body. In `filter` mode the predicate carries provider_id and is
 * sent verbatim — NO ids are enumerated client-side, which is the whole point of the escalation. */
export function buildBulkRequest(
  providerId: string,
  filter: KeyFilter,
  scope: BulkScope,
  op: BulkOp,
  opts?: { reason?: string; preview?: boolean },
): BulkRequest {
  const base: BulkRequest = { provider_id: providerId, op };
  if (opts?.reason) base.reason = opts.reason;
  if (opts?.preview) base.preview = true;
  if (scope.mode === "ids") {
    return { ...base, ids: [...scope.ids] };
  }
  return { ...base, filter: { ...filter, provider_id: providerId } };
}

/** The count request that backs both aria-rowcount and the "Select all N matching filter" label
 * (doc 04 §4.2: preview:true → 200 {matched:N}, creates nothing). op is required by the schema
 * but no state changes under preview, so a benign op is used purely to obtain the count. */
export function buildPreviewRequest(providerId: string, filter: KeyFilter): BulkRequest {
  return buildBulkRequest(providerId, filter, { mode: "filter" }, "disable", { preview: true });
}

/** op=delete routes through the approvals gate (key_bulk_delete) → 202 {approval_request_id}. */
export function isApprovalGatedOp(op: BulkOp): boolean {
  return op === "delete";
}

export interface Escalation {
  /** rows currently checked on the loaded page. */
  pageSelected: number;
  /** total rows matching the active filter (from the preview count). */
  totalMatching: number;
  /** true once the operator clicked "select all matching filter". */
  allMatching: boolean;
}

/** The bulk-bar summary label + the scope that will actually be submitted. */
export function resolveScope(
  selectedIds: ReadonlySet<string>,
  esc: Pick<Escalation, "allMatching">,
): BulkScope {
  return esc.allMatching ? { mode: "filter" } : { mode: "ids", ids: [...selectedIds] };
}
