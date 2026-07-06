// features/keys — the VIRTUALIZED key grid (doc 09 §3.1, P9 acceptance #1). @tanstack/react-table
// (headless column/row model) + @tanstack/react-virtual (windowing) over useInfiniteQuery cursor
// pages; server-side sort/filter. aria-rowcount comes from the server total (the preview match
// count), aria-rowindex is header-inclusive 1-based — so AT announces "row 512 of 4,213" even
// though only a window is in the DOM. Fetches ahead on scroll to stream 1,000+ rows smoothly.
import { useMemo, useRef } from "react";
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import { EmptyState } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { formatCount, formatLatencyMs, formatPercent, relativeTime } from "../../lib/format";
import { KeyHealthBadge, KeyStatusBadge } from "./statusCells";
import { useProviderKeys } from "./api";
import { ariaRowIndex, flattenKeyPages, gridAriaRowCount, shouldFetchNext } from "./keyGridModel";
import type { KeyFilter, ProviderKey } from "./types";

const ROW_H = 40;
const SORT_WHITELIST = new Set(["last_used_at", "label", "weight"]);

function CreditsBar({ remaining, limit }: { remaining?: number; limit?: number }) {
  if (remaining === undefined) return <span>—</span>;
  const pct = limit && limit > 0 ? Math.max(0, Math.min(100, (remaining / limit) * 100)) : null;
  return (
    <span title={formatCount(remaining)}>
      {pct !== null ? (
        <span className="credits-bar" aria-hidden="true">
          <span style={{ width: `${pct}%` }} />
        </span>
      ) : (
        formatCount(remaining)
      )}
    </span>
  );
}

function buildColumns(): ColumnDef<ProviderKey, unknown>[] {
  return [
    { id: "label", header: "Label", cell: (c) => c.row.original.label },
    {
      id: "secret_last4",
      header: "Last4",
      cell: (c) => <code>*{c.row.original.secret_last4 ?? "····"}</code>,
    },
    { id: "status", header: "Status", cell: (c) => <KeyStatusBadge status={c.row.original.status} /> },
    { id: "health", header: "Health", cell: (c) => <KeyHealthBadge health={c.row.original.health} /> },
    { id: "pool", header: "Pool", cell: (c) => c.row.original.pool ?? "—" },
    { id: "region", header: "Region", cell: (c) => c.row.original.region ?? "—" },
    { id: "environment", header: "Env", cell: (c) => c.row.original.environment ?? "—" },
    {
      id: "credits",
      header: "Credits",
      cell: (c) => (
        <CreditsBar remaining={c.row.original.credits_remaining} limit={c.row.original.credits_limit} />
      ),
    },
    { id: "usage_today", header: "Today", cell: (c) => formatCount(c.row.original.usage_today) },
    { id: "success_ewma", header: "Succ", cell: (c) => formatPercent(c.row.original.success_ewma) },
    { id: "latency_ewma_ms", header: "Lat", cell: (c) => formatLatencyMs(c.row.original.latency_ewma_ms) },
    { id: "expires_at", header: "Expires", cell: (c) => c.row.original.expires_at ?? "—" },
    {
      id: "last_used_at",
      header: "Last used",
      cell: (c) => (c.row.original.last_used_at ? relativeTime(c.row.original.last_used_at) : "—"),
    },
  ];
}

export interface KeyGridProps {
  providerId: string;
  filter: KeyFilter;
  sort: string;
  onSortChange: (s: string) => void;
  serverTotal?: number;
  selection: ReadonlySet<string>;
  onSelectionChange: (ids: ReadonlySet<string>) => void;
  allMatching: boolean;
  onRowOpen: (key: ProviderKey) => void;
}

export function KeyGrid(props: KeyGridProps) {
  const { providerId, filter, sort, selection, allMatching, onRowOpen } = props;
  const q = useProviderKeys(providerId, filter, sort);
  const data = useMemo(() => flattenKeyPages(q.data?.pages), [q.data]);
  const columns = useMemo(buildColumns, []);
  const scrollRef = useRef<HTMLDivElement>(null);

  const table = useReactTable({
    data,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getRowId: (r) => r.id,
    manualSorting: true,
  });
  const rows = table.getRowModel().rows;

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_H,
    overscan: 12,
  });

  const sortField = sort.startsWith("-") ? sort.slice(1) : sort;
  const sortDesc = sort.startsWith("-");
  function cycleSort(field: string) {
    if (!SORT_WHITELIST.has(field)) return;
    if (sortField !== field) props.onSortChange(field);
    else if (!sortDesc) props.onSortChange(`-${field}`);
    else props.onSortChange("");
  }

  function toggle(id: string) {
    const next = new Set(selection);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    props.onSelectionChange(next);
  }
  function toggleAllOnPage(check: boolean) {
    const next = new Set(selection);
    for (const r of rows) if (check) next.add(r.id);
      else next.delete(r.id);
    props.onSelectionChange(next);
  }
  const allPageChecked = rows.length > 0 && rows.every((r) => selection.has(r.id));

  function onScroll() {
    const el = scrollRef.current;
    if (!el) return;
    if (
      shouldFetchNext({
        scrollTop: el.scrollTop,
        scrollHeight: el.scrollHeight,
        clientHeight: el.clientHeight,
        hasNextPage: q.hasNextPage ?? false,
        isFetching: q.isFetchingNextPage,
      })
    ) {
      void q.fetchNextPage();
    }
  }

  if (q.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load keys"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }}
      />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 480 }} aria-busy="true" aria-label="Loading keys" />;

  const virtualRows = virtualizer.getVirtualItems();
  const totalHeight = virtualizer.getTotalSize();
  const padTop = virtualRows.length ? virtualRows[0]!.start : 0;
  const padBottom = virtualRows.length ? totalHeight - virtualRows[virtualRows.length - 1]!.end : 0;
  const colSpan = columns.length + 1;
  const rowCount = gridAriaRowCount(props.serverTotal, rows.length);

  if (rows.length === 0) {
    return (
      <EmptyState
        variant={Object.keys(filter).length ? "zero-results" : "zero-data"}
        title={Object.keys(filter).length ? "No keys match filters" : "No provider keys yet"}
        body={Object.keys(filter).length ? undefined : "Import keys to get started."}
        action={
          Object.keys(filter).length ? undefined : { label: "Import keys", href: "/keys/import" }
        }
      />
    );
  }

  return (
    <div className="p-table-wrap key-grid" ref={scrollRef} onScroll={onScroll} style={{ maxHeight: "62vh" }}>
      <table className="p-table" aria-rowcount={rowCount} aria-multiselectable="true">
        <caption className="p-visually-hidden">Provider keys ({rowCount} total)</caption>
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id}>
              <th scope="col">
                <input
                  type="checkbox"
                  aria-label="Select all on page"
                  checked={allMatching || allPageChecked}
                  disabled={allMatching}
                  onChange={(e) => toggleAllOnPage(e.currentTarget.checked)}
                />
              </th>
              {hg.headers.map((header) => {
                const field = header.column.id;
                const sortable = SORT_WHITELIST.has(field);
                const isSorted = sortField === field;
                return (
                  <th key={header.id} scope="col" aria-sort={isSorted ? (sortDesc ? "descending" : "ascending") : undefined}>
                    {sortable ? (
                      <button onClick={() => cycleSort(field)}>
                        {flexRender(header.column.columnDef.header, header.getContext())}
                        {isSorted ? <span aria-hidden="true">{sortDesc ? " ↓" : " ↑"}</span> : null}
                      </button>
                    ) : (
                      flexRender(header.column.columnDef.header, header.getContext())
                    )}
                  </th>
                );
              })}
            </tr>
          ))}
        </thead>
        <tbody>
          {padTop > 0 ? (
            <tr aria-hidden="true">
              <td colSpan={colSpan} style={{ height: padTop, padding: 0, border: 0 }} />
            </tr>
          ) : null}
          {virtualRows.map((v) => {
            const row = rows[v.index]!;
            const key = row.original;
            const checked = allMatching || selection.has(row.id);
            return (
              <tr
                key={row.id}
                aria-selected={checked}
                aria-rowindex={ariaRowIndex(v.index)}
                style={{ height: ROW_H }}
              >
                <td>
                  <input
                    type="checkbox"
                    aria-label={`Select ${key.label}`}
                    checked={checked}
                    disabled={allMatching}
                    onChange={() => toggle(row.id)}
                  />
                </td>
                {row.getVisibleCells().map((cell) => (
                  <td key={cell.id} onClick={() => onRowOpen(key)}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                ))}
              </tr>
            );
          })}
          {padBottom > 0 ? (
            <tr aria-hidden="true">
              <td colSpan={colSpan} style={{ height: padBottom, padding: 0, border: 0 }} />
            </tr>
          ) : null}
        </tbody>
      </table>
      {q.isFetchingNextPage ? (
        <div className="grid-loading" role="status">Loading more…</div>
      ) : null}
    </div>
  );
}
