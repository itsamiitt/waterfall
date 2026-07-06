// features/cost/query.ts — the ONE query-string builder shared by the summary read and the
// NDJSON export (doc 04 §2.10: export is WYSIWYG — identical filter params to /cost/summary).
// Keeping a single builder is what makes the export provably carry the on-screen filters.
import { listQuery } from "../../lib/cursors";
import type { CostFilters } from "./types";

/** Build the `?group_by=&from=&to=&filter[dim]=v` query for a cost view (doc 04 §2.10). */
export function buildCostQuery(f: CostFilters): string {
  const params: Record<string, string> = { group_by: f.group_by };
  if (f.from) params.from = f.from;
  if (f.to) params.to = f.to;
  for (const [dim, value] of Object.entries(f.filter)) {
    if (value) params[`filter[${dim}]`] = value;
  }
  return listQuery(params);
}

/** Summary read path — same query as the export (WYSIWYG contract). */
export function costSummaryPath(f: CostFilters): string {
  return `/cost/summary${buildCostQuery(f)}`;
}

/** Export stream path — MUST match the summary query exactly (doc 04 §2.10). */
export function costExportPath(f: CostFilters): string {
  return `/cost/export${buildCostQuery(f)}`;
}
