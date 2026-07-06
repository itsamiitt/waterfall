// features/workflows/WorkflowListPage.tsx — /workflows. Denormalized workflow_index list with a
// trigger filter (doc 04 §2.7, doc 09 §7). Each row deep-links to the builder.
import { useState } from "react";
import { Link } from "react-router";
import { isApiError } from "../../api/client";
import { Badge, EmptyState, Select, Table, type ColumnDef } from "../../design/primitives";
import { RequireRole } from "../../app/guards";
import { useSseTopics } from "../../api/sse";
import { useWorkflowIndex } from "./api";
import type { Trigger, WorkflowIndexItem } from "./types";

const TRIGGERS: { value: Trigger | ""; label: string }[] = [
  { value: "", label: "All triggers" },
  { value: "api", label: "api" },
  { value: "batch", label: "batch" },
  { value: "webhook", label: "webhook" },
];

const columns: ColumnDef<WorkflowIndexItem, unknown>[] = [
  {
    id: "name",
    header: "Workflow",
    cell: ({ row }) => (
      <Link to={`/workflows/${encodeURIComponent(row.original.scope_key)}/edit`}>{row.original.name}</Link>
    ),
  },
  { id: "scope_key", header: "Scope", cell: ({ row }) => <code>{row.original.scope_key}</code> },
  { id: "trigger", header: "Trigger", cell: ({ row }) => <Badge status="info" label={row.original.trigger} icon="dot" /> },
  { id: "active_version", header: "Active", cell: ({ row }) => `v${row.original.active_version ?? "—"}` },
  {
    id: "fields",
    header: "Fields",
    cell: ({ row }) => <span className="wf-muted">{(row.original.fields ?? []).join(", ") || "—"}</span>,
  },
];

export function WorkflowListPage() {
  useSseTopics(["approval"]);
  return (
    <RequireRole group="workflows.read">
      <List />
    </RequireRole>
  );
}

function List() {
  const [trigger, setTrigger] = useState<Trigger | "">("");
  const q = useWorkflowIndex(trigger ? { trigger } : undefined);

  return (
    <>
      <div className="page-header">
        <h1>Waterfall configurations</h1>
        <span className="page-header-meta">one named Waterfall per scope</span>
      </div>
      <div className="wf-filters">
        <Select label="Trigger" options={TRIGGERS} value={trigger} onChange={setTrigger} />
      </div>
      {q.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" />
      ) : q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load workflows"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.data.workflows.length === 0 ? (
        <EmptyState
          variant={trigger ? "zero-results" : "zero-data"}
          title={trigger ? "No workflows match this trigger" : "No Waterfall configurations yet"}
          body={trigger ? undefined : "Create a draft to define one for a scope."}
          action={trigger ? { label: "Clear filter", onClick: () => setTrigger("") } : { label: "Edit default", href: "/workflows/default/edit" }}
        />
      ) : (
        <Table columns={columns} data={q.data.workflows} getRowId={(w) => w.scope_key} caption="Workflows" />
      )}
    </>
  );
}
