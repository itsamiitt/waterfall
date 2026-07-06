// features/cost — Cost Analytics (doc 09 §10). StatTile row (incl. credits/successful-result
// carrying numerator+denominator), spend-by-group_by chart (lazy recharts), drill-down breakdown
// table (row click pushes the value into the filter and advances group_by), forecast band labeled
// modeled/indicative, and a WYSIWYG NDJSON export carrying the current filters. Every figure is
// modeled from rate cards (doc 04 §2.10) — budgets & rules alert, G4 ceilings enforce.
import { Suspense, lazy, useMemo, useState } from "react";
import { isApiError } from "../../api/client";
import { useAuth } from "../../app/guards";
import { toast } from "../../app/toast";
import {
  Badge,
  Button,
  EmptyState,
  Select,
  Table,
  type ColumnDef,
  type SelectOption,
} from "../../design/primitives";
import { formatCompact, formatCount } from "../../lib/format";
import { exportCostNdjson, useCostForecast, useCostPerEnrichment, useCostSummary } from "./api";
import { ForecastTile } from "./forecast";
import { DIM_FIELD, GROUP_BYS, type CostFilters, type CostItem, type GroupBy } from "./types";

const CostBars = lazy(() => import("./CostBars"));

/** Drill order: clicking a row advances to the next dimension (doc 09 §10.1). */
const DRILL_NEXT: Record<GroupBy, GroupBy | undefined> = {
  provider: "key",
  key: "workflow",
  workflow: "country",
  tenant: "provider",
  country: undefined,
};

function defaultRange(): { from: string; to: string } {
  const to = new Date();
  const from = new Date(to.getTime() - 30 * 24 * 60 * 60 * 1000);
  const iso = (d: Date) => d.toISOString().replace(/\.\d{3}Z$/, "Z");
  return { from: iso(from), to: iso(to) };
}

export default function CostPage() {
  const { role } = useAuth();
  const [filters, setFilters] = useState<CostFilters>(() => ({
    group_by: "provider",
    ...defaultRange(),
    filter: {},
  }));

  // group_by=key serves Class P key_usage_1d — operator-only (doc 04 §2.10).
  const groupByOpts: SelectOption<GroupBy>[] = GROUP_BYS.filter(
    (g) => g !== "key" || role === "operator",
  ).map((g) => ({ value: g, label: g }));

  const summary = useCostSummary(filters);
  const perEnrichment = useCostPerEnrichment(filters);
  const forecast = useCostForecast();
  const [exporting, setExporting] = useState(false);

  const items = summary.data?.items ?? [];
  const field = DIM_FIELD[filters.group_by];

  const columns = useMemo<ColumnDef<CostItem, unknown>[]>(
    () => [
      { id: "dim", header: filters.group_by, cell: (c) => String(c.row.original[field] ?? "—") },
      { id: "credits", header: "credits", cell: (c) => formatCount(c.row.original.credits) },
      { id: "calls", header: "calls", cell: (c) => formatCount(c.row.original.calls) },
      {
        id: "successful_results",
        header: "successful",
        cell: (c) => formatCount(c.row.original.successful_results),
      },
      {
        id: "credits_per_call",
        header: "cr/call",
        cell: (c) => c.row.original.credits_per_call?.toFixed(3) ?? "—",
      },
      {
        id: "credits_per_successful_result",
        header: "cr/success",
        cell: (c) => c.row.original.credits_per_successful_result?.toFixed(3) ?? "—",
      },
    ],
    [filters.group_by, field],
  );

  function drillInto(item: CostItem) {
    const next = DRILL_NEXT[filters.group_by];
    if (!next) return;
    if (next === "key" && role !== "operator") return;
    const value = String(item[field] ?? "");
    if (!value) return;
    setFilters((f) => ({ ...f, group_by: next, filter: { ...f.filter, [field]: value } }));
  }

  function clearFilter(dim: string) {
    setFilters((f) => {
      const filter = { ...f.filter };
      delete filter[dim];
      return { ...f, filter };
    });
  }

  async function onExport() {
    setExporting(true);
    try {
      await exportCostNdjson(filters);
    } catch (e) {
      toast.error(
        isApiError(e) ? `Export failed (${e.code})` : "Export failed — retry",
      );
    } finally {
      setExporting(false);
    }
  }

  const filterChips = Object.entries(filters.filter);
  const pe = perEnrichment.data;

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Cost analytics</h1>
        <span className="page-header-meta">
          <Badge status="info" icon="gauge" label="modeled from rate card" />
        </span>
      </div>

      <div className="filter-bar" style={{ display: "flex", gap: "var(--space-3)", alignItems: "flex-end", flexWrap: "wrap" }}>
        <Select
          label="Group by"
          options={groupByOpts}
          value={filters.group_by}
          onChange={(v) => setFilters((f) => ({ ...f, group_by: v, filter: {} }))}
        />
        {filterChips.map(([dim, value]) => (
          <Button key={dim} size="sm" onClick={() => clearFilter(dim)} iconStart={<span aria-hidden>×</span>}>
            {dim}: {value}
          </Button>
        ))}
        <span style={{ flex: 1 }} />
        <Button variant="secondary" onClick={() => void onExport()} loading={exporting}>
          Export NDJSON
        </Button>
      </div>

      {filters.group_by === "key" || filters.filter["key_id"] ? (
        <p style={{ color: "var(--color-text-muted)", fontSize: "var(--text-sm)" }}>
          key → workflow drill-down is bounded by the 48h usage-events window (doc 04 §2.10).
        </p>
      ) : null}

      <div className="tile-grid" style={{ marginBottom: "var(--space-5)" }}>
        {forecast.isSuccess ? <ForecastTile forecast={forecast.data} /> : null}
        <div className="p-stattile">
          <span className="p-stattile-label">Cost per successful result</span>
          <span className="p-stattile-value">
            {pe?.credits_per_successful_result?.toFixed(3) ?? "—"}
            <span className="p-stattile-unit">credits</span>
          </span>
          <span className="p-stattile-delta">
            {pe ? `${formatCount(pe.credits)} credits / ${formatCount(pe.successful_results)} results` : "—"}
          </span>
        </div>
        <div className="p-stattile">
          <span className="p-stattile-label">Cost per call</span>
          <span className="p-stattile-value">
            {pe?.credits_per_call?.toFixed(3) ?? "—"}
            <span className="p-stattile-unit">credits</span>
          </span>
          <span className="p-stattile-delta">
            {pe ? `${formatCount(pe.credits)} credits / ${formatCount(pe.calls)} calls` : "—"}
          </span>
        </div>
        <div className="p-stattile">
          <span className="p-stattile-label">Modeled credits (range)</span>
          <span className="p-stattile-value">{formatCompact(pe?.credits)}</span>
          <span className="p-stattile-delta">source: modeled</span>
        </div>
      </div>

      {summary.isError ? (
        <EmptyState
          variant="error"
          title="Could not load cost summary"
          errorCode={isApiError(summary.error) ? summary.error.code : undefined}
          body={summary.error instanceof Error ? summary.error.message : undefined}
          action={{ label: "Retry", onClick: () => void summary.refetch() }}
        />
      ) : summary.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading cost" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-data" title="No usage recorded in this range" />
      ) : (
        <>
          <Suspense fallback={<div className="skeleton" style={{ height: 220 }} aria-hidden="true" />}>
            <CostBars items={items} groupBy={filters.group_by} />
          </Suspense>
          <Table
            columns={columns}
            data={items}
            getRowId={(r) => String(r[field] ?? JSON.stringify(r))}
            onRowActivate={drillInto}
            rowActions={(r) =>
              DRILL_NEXT[filters.group_by] ? (
                <Button size="sm" variant="ghost" onClick={() => drillInto(r)} aria-label="Drill down">
                  ›
                </Button>
              ) : null
            }
            caption="Cost breakdown by dimension"
          />
        </>
      )}
    </>
  );
}
