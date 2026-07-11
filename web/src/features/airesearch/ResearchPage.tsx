// features/airesearch — AI Research dossiers (docs/research-intelligence/06, 08). Lists the Tenant's
// assembled dossiers (freshest first); activating a row opens the full stored Dossier document. The
// dossier is a research-owned composite — the admin surface reads it, never assembles it.
import { useNavigate } from "react-router";
import { isApiError } from "../../api/client";
import { EmptyState, Table, type ColumnDef } from "../../design/primitives";
import { formatUtc } from "../../lib/format";
import { useDossiers } from "./api";
import type { DossierSummary } from "./types";

const columns: ColumnDef<DossierSummary, unknown>[] = [
  { id: "subject_key", header: "subject", cell: (c) => c.row.original.subject_key },
  { id: "dossier_id", header: "dossier id", cell: (c) => c.row.original.dossier_id },
  { id: "overall_confidence", header: "confidence", cell: (c) => c.row.original.overall_confidence.toFixed(2) },
  { id: "config_version", header: "config", cell: (c) => c.row.original.config_version || "—" },
  { id: "freshness_at", header: "freshness", cell: (c) => formatUtc(c.row.original.freshness_at) },
];

export default function ResearchPage() {
  const nav = useNavigate();
  const q = useDossiers();
  const items = q.data?.items ?? [];

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Research</h1>
        <span className="page-header-meta">assembled dossiers (domain → Dossier)</span>
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load dossiers"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading dossiers" />
      ) : items.length === 0 ? (
        <EmptyState
          variant="zero-data"
          title="No dossiers assembled yet"
          body="Submit a research run (POST /v1/research) to assemble one."
        />
      ) : (
        <Table
          columns={columns}
          data={items}
          getRowId={(r) => r.dossier_id}
          onRowActivate={(r) => nav(`/research/${encodeURIComponent(r.dossier_id)}`)}
          caption="Assembled dossiers"
        />
      )}
    </>
  );
}
