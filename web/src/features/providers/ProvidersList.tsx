// features/providers — catalog list (doc 09 §2.1). Screen → endpoint: GET /providers (cursor
// list; server-side filter/sort). Rows carry the DUAL badge (ProviderBadges): inclusion chip +
// op_state chip + server-computed effective_available — never conflated (P9 acceptance #4).
import { useMemo, useState } from "react";
import { Link } from "react-router";
import { useSseTopics } from "../../api/sse";
import { isApiError } from "../../api/client";
import {
  Button,
  EmptyState,
  Select,
  Table,
  type ColumnDef,
  type SelectOption,
} from "../../design/primitives";
import { formatCount, formatLatencyMs, formatPercent } from "../../lib/format";
import { flattenPages } from "../../lib/cursors";
import { INCLUSION_STATUSES, OP_STATES } from "../../lib/status";
import { ProviderBadges } from "./badges";
import { useProviders } from "./api";
import type { Provider, ProviderFilter } from "./types";

const SORT_WHITELIST = new Set(["priority", "health_score", "credits_remaining", "created_at"]);

const statusOpts: SelectOption[] = INCLUSION_STATUSES.map((s) => ({ value: s, label: s }));
const opStateOpts: SelectOption[] = OP_STATES.map((s) => ({ value: s, label: s }));

function columns(): ColumnDef<Provider, unknown>[] {
  return [
    {
      id: "display_name",
      header: "Provider",
      cell: (c) => {
        const p = c.row.original;
        return <Link to={`/providers/${encodeURIComponent(p.id)}/config`}>{p.display_name}</Link>;
      },
    },
    { id: "category", header: "Category", cell: (c) => c.row.original.category ?? "—" },
    {
      id: "status",
      header: "Status / availability",
      cell: (c) => {
        const p = c.row.original;
        return (
          <ProviderBadges
            status={p.status}
            opState={p.op_state}
            effectiveAvailable={p.effective_available}
            unavailableReason={p.unavailable_reason}
          />
        );
      },
    },
    { id: "health_score", header: "Health", cell: (c) => formatPercent(c.row.original.health_score) },
    { id: "priority", header: "Priority", cell: (c) => c.row.original.priority ?? "—" },
    {
      id: "credits_remaining",
      header: "Credits",
      cell: (c) => formatCount(c.row.original.credits_remaining),
    },
    { id: "latency", header: "Latency", cell: (c) => formatLatencyMs(c.row.original.avg_latency_ms) },
    { id: "sunset_at", header: "Sunset", cell: (c) => c.row.original.sunset_at ?? "—" },
  ];
}

export default function ProvidersList() {
  useSseTopics(["provider"]); // provider.health.changed invalidates the list (doc 09 §2.2)
  const [filter, setFilter] = useState<ProviderFilter>({});
  const [sort, setSort] = useState("priority");
  const q = useProviders(filter, sort);
  const cols = useMemo(columns, []);
  const rows = flattenPages(q.data?.pages);

  const setF = (k: keyof ProviderFilter, v: string) =>
    setFilter((f) => ({ ...f, [k]: v || undefined }));

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Providers</h1>
      </div>

      <div className="filter-bar">
        <Select label="Status" options={statusOpts} value={filter.status ?? ""} placeholder="any"
          onChange={(v) => setF("status", v)} />
        <Select label="Op state" options={opStateOpts} value={filter.op_state ?? ""} placeholder="any"
          onChange={(v) => setF("op_state", v)} />
        {Object.keys(filter).length > 0 ? (
          <Button size="sm" onClick={() => setFilter({})}>Clear filters</Button>
        ) : null}
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load providers"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading providers" />
      ) : rows.length === 0 ? (
        <EmptyState
          variant={Object.keys(filter).length ? "zero-results" : "zero-data"}
          title={Object.keys(filter).length ? "No providers match filters" : "No providers in the catalog"}
          action={
            Object.keys(filter).length
              ? { label: "Clear filters", onClick: () => setFilter({}) }
              : undefined
          }
        />
      ) : (
        <>
          <Table
            columns={cols}
            data={rows}
            virtualized={rows.length > 100}
            getRowId={(r) => r.id}
            sort={sort}
            onSortChange={(s) => {
              const field = s.startsWith("-") ? s.slice(1) : s;
              if (s === "" || SORT_WHITELIST.has(field)) setSort(s);
            }}
            caption="Provider catalog"
          />
          {q.hasNextPage ? (
            <div style={{ marginTop: "var(--space-4)" }}>
              <Button onClick={() => void q.fetchNextPage()} loading={q.isFetchingNextPage}>
                Load more
              </Button>
            </div>
          ) : null}
        </>
      )}
    </>
  );
}
