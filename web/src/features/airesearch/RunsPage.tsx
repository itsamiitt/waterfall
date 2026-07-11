// features/airesearch — async research run monitor (docs/research-intelligence/08, ADR-0028). A read-only
// table of the Tenant's research runs and their lifecycle status (queued → running → done|failed). A run
// is created by POST /v1/research (202 + run_id); the background worker transitions it.
import { useNavigate } from "react-router";
import { isApiError } from "../../api/client";
import { Button, EmptyState, Table, type ColumnDef } from "../../design/primitives";
import { formatUtc } from "../../lib/format";
import { useResearchRuns } from "./api";
import { isActiveRun } from "./logic";
import type { RunSummary } from "./types";

const columns: ColumnDef<RunSummary, unknown>[] = [
  { id: "run_id", header: "run", cell: (c) => c.row.original.run_id },
  { id: "subject_key", header: "subject", cell: (c) => c.row.original.subject_key },
  {
    id: "status",
    header: "status",
    cell: (c) => `${c.row.original.status}${isActiveRun(c.row.original.status) ? " …" : ""}`,
  },
  { id: "updated_at", header: "updated", cell: (c) => formatUtc(c.row.original.updated_at) },
];

export default function RunsPage() {
  const nav = useNavigate();
  const q = useResearchRuns();
  const items = q.data?.items ?? [];

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Research runs</h1>
        <span className="page-header-meta">
          <Button size="sm" variant="ghost" onClick={() => nav("/research")}>
            ← dossiers
          </Button>
        </span>
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load research runs"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading research runs" />
      ) : items.length === 0 ? (
        <EmptyState
          variant="zero-data"
          title="No research runs yet"
          body="Submit POST /v1/research to start an async run; it appears here as it is processed."
        />
      ) : (
        <Table columns={columns} data={items} getRowId={(r) => r.run_id} caption="Research runs" />
      )}
    </>
  );
}
