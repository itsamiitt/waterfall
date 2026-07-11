// features/aimodels — the LLM model cascade catalog (docs/research-intelligence/04, 08). A read-only
// table of the platform ai.Models registry in cascade order (free-first): slug, gateway model id,
// wire dialect, inclusion status, tier, placeholder cost, and host. Platform config — operator-only.
import { isApiError } from "../../api/client";
import { EmptyState, Table, type ColumnDef } from "../../design/primitives";
import { useAIModels } from "./api";
import { modelCost } from "./logic";
import type { ModelInfo } from "./types";

const columns: ColumnDef<ModelInfo, unknown>[] = [
  { id: "slug", header: "slug", cell: (c) => c.row.original.slug },
  { id: "model_id", header: "model", cell: (c) => c.row.original.model_id },
  { id: "dialect", header: "dialect", cell: (c) => c.row.original.dialect },
  { id: "tier", header: "tier", cell: (c) => (c.row.original.free ? "free" : "paid") },
  { id: "cost", header: "cost", cell: (c) => modelCost(c.row.original) },
  { id: "status", header: "inclusion", cell: (c) => c.row.original.status },
  { id: "host", header: "host", cell: (c) => c.row.original.host },
];

export default function AIModelsPage() {
  const q = useAIModels();
  const items = q.data?.items ?? [];

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>AI models</h1>
        <span className="page-header-meta">LLM cascade catalog (free-first; platform config)</span>
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load AI models"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading models" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-data" title="No models registered" />
      ) : (
        <Table columns={columns} data={items} getRowId={(r) => r.slug} caption="LLM cascade catalog" />
      )}
    </>
  );
}
