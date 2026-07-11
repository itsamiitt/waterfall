// features/intent — Computed Intent (docs/research-intelligence/05, 08). Lists the Tenant's accounts
// with their strongest computed intent class; activating a row opens the per-class breakdown. These
// per-class scores are the explainable model output — distinct from the single intent_score Field
// written back to enrichment (never conflated, doc 05).
import { useNavigate } from "react-router";
import { isApiError } from "../../api/client";
import { EmptyState, Table, type ColumnDef } from "../../design/primitives";
import { useIntentAccounts } from "./api";
import type { AccountSummary } from "./types";

const columns: ColumnDef<AccountSummary, unknown>[] = [
  { id: "account", header: "account", cell: (c) => c.row.original.account },
  { id: "top_class", header: "top class", cell: (c) => c.row.original.top_class || "—" },
  { id: "top_score", header: "score", cell: (c) => c.row.original.top_score.toFixed(3) },
  { id: "classes", header: "classes", cell: (c) => String(c.row.original.classes) },
];

export default function IntentPage() {
  const nav = useNavigate();
  const q = useIntentAccounts();
  const items = q.data?.items ?? [];

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Intent</h1>
        <span className="page-header-meta">computed per-account intent (async)</span>
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load intent accounts"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading intent" />
      ) : items.length === 0 ? (
        <EmptyState
          variant="zero-data"
          title="No accounts have computed intent yet"
          body="Trigger an intent refresh to populate per-account scores."
        />
      ) : (
        <Table
          columns={columns}
          data={items}
          getRowId={(r) => r.account}
          onRowActivate={(r) => nav(`/intent/${encodeURIComponent(r.account)}`)}
          caption="Accounts with computed intent"
        />
      )}
    </>
  );
}
