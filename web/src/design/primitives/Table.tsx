// Table primitive: @tanstack/react-table headless core + @tanstack/react-virtual windowing
// (doc 08 §6.2). Sorting is SERVER-side: `sort`/`onSortChange` round-trip doc 04 §1.5 `?sort=`
// strings; the table never sorts rows itself. `virtualized` is mandatory for grids that can
// exceed 100 rows (doc 08 §10); aria-rowcount comes from server totals (doc 08 §9).
import { useRef, type ReactNode } from "react";
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";

export type { ColumnDef };

/** doc 04 §1.5 sort string: "<field>" ascending, "-<field>" descending, "" for default. */
export type SortSpec = string;

export interface TableProps<Row> {
  columns: ColumnDef<Row, unknown>[];
  data: Row[];
  sort?: SortSpec;
  onSortChange?: (sort: SortSpec) => void;
  selection?: ReadonlySet<string>;
  onSelectionChange?: (ids: ReadonlySet<string>) => void;
  getRowId?: (row: Row) => string;
  virtualized?: boolean;
  rowHeight?: number;
  /** Server-reported total row count (feeds aria-rowcount under virtualization). */
  ariaRowCount?: number;
  rowActions?: (row: Row) => ReactNode;
  onRowActivate?: (row: Row) => void;
  caption?: string;
}

export function Table<Row>({
  columns,
  data,
  sort = "",
  onSortChange,
  selection,
  onSelectionChange,
  getRowId,
  virtualized = false,
  rowHeight = 36,
  ariaRowCount,
  rowActions,
  onRowActivate,
  caption,
}: TableProps<Row>) {
  const table = useReactTable({
    data,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getRowId,
    manualSorting: true,
  });

  const scrollRef = useRef<HTMLDivElement>(null);
  const rows = table.getRowModel().rows;
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 10,
    enabled: virtualized,
  });

  const sortField = sort.startsWith("-") ? sort.slice(1) : sort;
  const sortDesc = sort.startsWith("-");

  function cycleSort(field: string) {
    if (!onSortChange) return;
    if (sortField !== field) onSortChange(field);
    else if (!sortDesc) onSortChange(`-${field}`);
    else onSortChange("");
  }

  function toggleRow(id: string) {
    if (!onSelectionChange || !selection) return;
    const next = new Set(selection);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    onSelectionChange(next);
  }

  const virtualRows = virtualized ? virtualizer.getVirtualItems() : null;
  const totalHeight = virtualized ? virtualizer.getTotalSize() : undefined;
  const paddingTop = virtualRows?.length ? virtualRows[0]!.start : 0;
  const paddingBottom =
    virtualRows?.length && totalHeight !== undefined
      ? totalHeight - virtualRows[virtualRows.length - 1]!.end
      : 0;

  const renderRow = (row: (typeof rows)[number], ariaRowIndex?: number) => {
    const selected = selection?.has(row.id) ?? false;
    return (
      <tr
        key={row.id}
        aria-selected={selection ? selected : undefined}
        aria-rowindex={ariaRowIndex}
        onClick={selection ? () => toggleRow(row.id) : undefined}
        onDoubleClick={onRowActivate ? () => onRowActivate(row.original) : undefined}
      >
        {row.getVisibleCells().map((cell) => (
          <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
        ))}
        {rowActions ? <td>{rowActions(row.original)}</td> : null}
      </tr>
    );
  };

  return (
    <div className="p-table-wrap" ref={scrollRef}>
      <table
        className="p-table"
        aria-rowcount={ariaRowCount}
        aria-multiselectable={selection ? true : undefined}
      >
        {caption ? <caption className="p-visually-hidden">{caption}</caption> : null}
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id}>
              {hg.headers.map((header) => {
                const field = header.column.id;
                const isSorted = sortField === field;
                const ariaSort = isSorted ? (sortDesc ? "descending" : "ascending") : undefined;
                return (
                  <th key={header.id} aria-sort={ariaSort} scope="col">
                    {onSortChange ? (
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
              {rowActions ? (
                <th scope="col">
                  <span className="p-visually-hidden">Actions</span>
                </th>
              ) : null}
            </tr>
          ))}
        </thead>
        <tbody>
          {virtualized && virtualRows ? (
            <>
              {paddingTop > 0 ? (
                <tr aria-hidden="true">
                  <td colSpan={columns.length + (rowActions ? 1 : 0)} style={{ height: paddingTop, padding: 0, border: 0 }} />
                </tr>
              ) : null}
              {virtualRows.map((v) => renderRow(rows[v.index]!, v.index + 2))}
              {paddingBottom > 0 ? (
                <tr aria-hidden="true">
                  <td colSpan={columns.length + (rowActions ? 1 : 0)} style={{ height: paddingBottom, padding: 0, border: 0 }} />
                </tr>
              ) : null}
            </>
          ) : (
            rows.map((r) => renderRow(r))
          )}
        </tbody>
      </table>
    </div>
  );
}
