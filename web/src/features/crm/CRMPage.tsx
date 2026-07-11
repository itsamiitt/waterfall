// features/crm — CRM outbound connections (docs/research-intelligence/08, ADR-0030). A read-only table of
// the Tenant's configured CRM push targets (provider, status, timestamps). Credential material is never
// shown — the backend projection omits secret_ref/config. Configuring a connection is a follow-on.
import { isApiError } from "../../api/client";
import { EmptyState, Table, type ColumnDef } from "../../design/primitives";
import { formatUtc } from "../../lib/format";
import { useCRMConnections } from "./api";
import { providerLabel } from "./logic";
import type { ConnectionSummary } from "./types";

const columns: ColumnDef<ConnectionSummary, unknown>[] = [
  { id: "connection_id", header: "connection", cell: (c) => c.row.original.connection_id },
  { id: "provider", header: "CRM", cell: (c) => providerLabel(c.row.original.provider) },
  { id: "status", header: "status", cell: (c) => c.row.original.status },
  { id: "updated_at", header: "updated", cell: (c) => formatUtc(c.row.original.updated_at) },
];

export default function CRMPage() {
  const q = useCRMConnections();
  const items = q.data?.items ?? [];

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>CRM connections</h1>
        <span className="page-header-meta">outbound push targets (credentials never shown)</span>
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load CRM connections"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading CRM connections" />
      ) : items.length === 0 ? (
        <EmptyState
          variant="zero-data"
          title="No CRM connections configured"
          body="Configure a CRM connection to push enriched accounts and contacts outbound."
        />
      ) : (
        <Table columns={columns} data={items} getRowId={(r) => r.connection_id} caption="CRM connections" />
      )}
    </>
  );
}
