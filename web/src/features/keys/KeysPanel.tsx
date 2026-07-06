// features/keys — composition for one provider's keys: filters + virtualized grid + bulk bar +
// row drawer + shared progress drawer. Reused by the /keys page (provider selector drives
// providerId) and the Provider detail "keys" tab (fixed providerId). Live via the `key` SSE topic
// (key.status.changed recolors a status pill without polling — doc 09 §3.3).
import { useState } from "react";
import { useSseTopics } from "../../api/sse";
import { Button } from "../../design/primitives";
import { flattenKeyPages } from "./keyGridModel";
import { exportFilename, keysToCsv, downloadCsv } from "./keyExport";
import { KeyFilters } from "./KeyFilters";
import { KeyGrid } from "./KeyGrid";
import { BulkBar } from "./BulkBar";
import { KeyDrawer } from "./KeyDrawer";
import { ProgressDrawer } from "./ProgressDrawer";
import { useKeyMatchCount, useProviderKeys } from "./api";
import type { KeyFilter, ProviderKey } from "./types";

export function KeysPanel({ providerId }: { providerId: string }) {
  useSseTopics(["key"]);
  const [filter, setFilter] = useState<KeyFilter>({});
  const [sort, setSort] = useState("-last_used_at");
  const [selection, setSelection] = useState<ReadonlySet<string>>(new Set());
  const [allMatching, setAllMatching] = useState(false);
  const [openKeyId, setOpenKeyId] = useState<string | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);

  const count = useKeyMatchCount(providerId, filter);
  const serverTotal = count.data?.matched;
  // Shared with KeyGrid via the identical query key (React Query dedupes) — Export reads these
  // already-loaded rows, fetching nothing more (doc 09 §3.1 WYSIWYG export).
  const gridQuery = useProviderKeys(providerId, filter, sort);
  const loadedRows = flattenKeyPages(gridQuery.data?.pages);

  function exportView() {
    const summary = Object.entries(filter)
      .filter(([, v]) => v !== undefined && (!Array.isArray(v) || v.length))
      .map(([k]) => k)
      .join("-");
    downloadCsv(exportFilename(providerId, summary, loadedRows.length), keysToCsv(loadedRows));
  }

  function changeFilter(f: KeyFilter) {
    setFilter(f);
    setSelection(new Set());
    setAllMatching(false);
  }
  function openRow(k: ProviderKey) {
    setOpenKeyId(k.id);
  }

  return (
    <div className="section">
      <div className="action-bar">
        <Button size="sm" onClick={exportView} disabled={loadedRows.length === 0}>
          Export view ({loadedRows.length} loaded)
        </Button>
      </div>
      <KeyFilters filter={filter} onChange={changeFilter} />
      <BulkBar
        providerId={providerId}
        filter={filter}
        selection={selection}
        serverTotal={serverTotal}
        allMatching={allMatching}
        onSelectAllMatching={() => setAllMatching(true)}
        onClearSelection={() => {
          setSelection(new Set());
          setAllMatching(false);
        }}
        onJob={(id) => setJobId(id)}
      />
      <KeyGrid
        providerId={providerId}
        filter={filter}
        sort={sort}
        onSortChange={setSort}
        serverTotal={serverTotal}
        selection={selection}
        onSelectionChange={(ids) => {
          setSelection(ids);
          if (allMatching) setAllMatching(false);
        }}
        allMatching={allMatching}
        onRowOpen={openRow}
      />
      <KeyDrawer keyId={openKeyId} open={openKeyId !== null} onClose={() => setOpenKeyId(null)} />
      <ProgressDrawer open={jobId !== null} onClose={() => setJobId(null)} jobId={jobId} kind="bulk" />
    </div>
  );
}
