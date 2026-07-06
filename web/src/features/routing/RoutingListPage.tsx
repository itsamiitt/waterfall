// features/routing/RoutingListPage.tsx — /routing. Scope list with active version + epoch and
// the resolver's effective tri-state overrides + source scope (doc 04 §2.7, doc 09 §6). Each
// scope deep-links to its editor.
import { Link } from "react-router";
import { isApiError } from "../../api/client";
import { Badge, EmptyState, Table, type ColumnDef } from "../../design/primitives";
import { RequireRole } from "../../app/guards";
import { useSseTopics } from "../../api/sse";
import { describeEffective, effectiveToken } from "./lifecycle";
import { useRoutingScopes } from "./api";
import type { RoutingScopeSummary } from "./types";

export function RoutingListPage() {
  useSseTopics(["approval"]);
  return (
    <RequireRole group="routing.read">
      <ScopeList />
    </RequireRole>
  );
}

const columns: ColumnDef<RoutingScopeSummary, unknown>[] = [
  {
    id: "scope_key",
    header: "Scope",
    cell: ({ row }) => (
      <Link to={`/routing/${encodeURIComponent(row.original.scope_key)}/edit`}>
        {row.original.scope_key}
      </Link>
    ),
  },
  { id: "active_version", header: "Active", cell: ({ row }) => `v${row.original.active_version ?? "—"}` },
  { id: "epoch", header: "Epoch", cell: ({ row }) => row.original.epoch },
  {
    id: "overrides",
    header: "Effective overrides (resolver)",
    cell: ({ row }) => {
      const overrides = Object.entries(row.original.overrides ?? {});
      if (overrides.length === 0) return <span className="rt-muted">—</span>;
      return (
        <div className="rt-effective-list">
          {overrides.slice(0, 4).map(([provider, o]) => (
            <span key={provider} className="rt-effective-chip" title={describeEffective(o)}>
              <Badge status={effectiveToken(o)} label={`${provider}: ${describeEffective(o)}`} icon="flag" family="outlined" />
            </span>
          ))}
        </div>
      );
    },
  },
];

function ScopeList() {
  const q = useRoutingScopes();

  if (q.isPending) {
    return (
      <>
        <div className="page-header">
          <h1>Routing policies</h1>
        </div>
        <div className="skeleton" style={{ height: 240 }} aria-busy="true" />
      </>
    );
  }
  if (q.isError) {
    return (
      <>
        <div className="page-header">
          <h1>Routing policies</h1>
        </div>
        <EmptyState
          variant="error"
          title="Could not load routing scopes"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      </>
    );
  }

  const scopes = q.data.scopes;
  return (
    <>
      <div className="page-header">
        <h1>Routing policies</h1>
        <span className="page-header-meta">
          8-level scope precedence · most-specific-wins (doc 07 §3)
        </span>
      </div>
      {scopes.length === 0 ? (
        <EmptyState
          variant="zero-data"
          title="No routing scopes yet"
          body="Create a draft for a scope to shape the Adaptive Router's proposal."
          action={{ label: "Edit default scope", href: "/routing/default/edit" }}
        />
      ) : (
        <Table columns={columns} data={scopes} getRowId={(s) => s.scope_key} caption="Routing scopes" />
      )}
    </>
  );
}
