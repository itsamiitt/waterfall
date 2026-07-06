// features/cost/api.ts — the ONLY place cost/budget endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §14 rows 27-29):
//   CostPage tiles/chart/table  GET /cost/summary, /cost/per-enrichment, /cost/forecast
//   Export button               GET /cost/export        (WYSIWYG NDJSON of the same filters)
//   BudgetsPage                 GET /budgets, PUT /budgets
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { API_BASE, ApiError, get, put } from "../../api/client";
import { staleTimes } from "../../api/keys";
import { costExportPath, costSummaryPath } from "./query";
import type {
  BudgetItem,
  BudgetsResponse,
  CostFilters,
  CostForecast,
  CostSummary,
  PerEnrichment,
} from "./types";

/** Feature-local query keys (mirrors the api/keys.ts convention without editing shared state). */
export const ck = {
  summary: (q: string) => ["cost", "summary", q] as const,
  perEnrichment: (q: string) => ["cost", "per-enrichment", q] as const,
  forecast: ["cost", "forecast"] as const,
  budgets: ["budgets"] as const,
};

/** GET /cost/summary — group-by spend; the query is byte-identical to the export (WYSIWYG). */
export function useCostSummary(filters: CostFilters) {
  return useQuery({
    queryKey: ck.summary(costSummaryPath(filters)),
    queryFn: () => get<CostSummary>(costSummaryPath(filters)),
    staleTime: staleTimes.telemetry,
  });
}

/** GET /cost/per-enrichment — credits/call + credits/successful-result (numerator+denominator). */
export function useCostPerEnrichment(filters: CostFilters) {
  const filterQuery = costSummaryPath(filters).slice("/cost/summary".length); // reuse from/to/filter
  return useQuery({
    queryKey: ck.perEnrichment(filterQuery),
    queryFn: () => get<PerEnrichment>("/cost/per-enrichment" + filterQuery),
    staleTime: staleTimes.telemetry,
  });
}

/** GET /cost/forecast — modeled EOM projection with an ~80% band (UNVERIFIED until backtested). */
export function useCostForecast() {
  return useQuery({
    queryKey: ck.forecast,
    queryFn: () => get<CostForecast>("/cost/forecast"),
    staleTime: staleTimes.telemetry,
  });
}

/** GET /budgets — budget objects with consumed_credits + alert_pct ladders (doc 04 §2.10). */
export function useBudgets() {
  return useQuery({
    queryKey: ck.budgets,
    queryFn: () => get<BudgetsResponse>("/budgets"),
    staleTime: staleTimes.config,
  });
}

/** PUT /budgets — full-replacement of the Tenant's budget set (doc 04 §2.10). */
export function useUpdateBudgets() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (items: BudgetItem[]) => put<BudgetsResponse>("/budgets", { items }),
    onSuccess: (data) => qc.setQueryData(ck.budgets, data),
  });
}

/** Stream GET /cost/export to a file, carrying the current filters verbatim (doc 04 §2.10).
 * Not routed through the JSON client: the response is an NDJSON attachment, not an envelope. */
export async function exportCostNdjson(filters: CostFilters): Promise<void> {
  const path = costExportPath(filters);
  const res = await fetch(API_BASE + path, { credentials: "include", headers: { Accept: "application/x-ndjson" } });
  if (!res.ok) {
    let code = "internal";
    try {
      code = (await res.json())?.error?.code ?? code;
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, code, `cost export failed (${res.status})`);
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `cost-export-${filters.group_by}.ndjson`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
