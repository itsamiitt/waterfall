// features/rotation — Key Pool detail (doc 09 §4.1). Composes strategy, members, selection-state
// (diagnostic) and simulate. Screen → endpoint: GET /key-pools/{id}; PUT …/members;
// DELETE /key-pools/{id} (409 conflict if referenced by an active routing policy → surfaced by
// the global mutation toast, doc 09 §4.2).
import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { Badge, Button, ConfirmDialog, EmptyState, Modal, Input } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { formatCount, formatLatencyMs, formatPercent } from "../../lib/format";
import { toast } from "../../app/toast";
import { StrategyForm } from "./StrategyForm";
import { SelectionStateView } from "./SelectionStateView";
import { SimulatePanel } from "./SimulatePanel";
import { useDeletePool, usePool, usePutMembers } from "./api";

export default function PoolDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const q = usePool(id);
  const putMembers = usePutMembers(id);
  const del = useDeletePool(id);
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [keyIds, setKeyIds] = useState("");

  if (q.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load pool"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }}
      />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 360 }} aria-busy="true" />;
  const pool = q.data;
  const members = pool.members ?? [];

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Pool · {pool.selector}</h1>
        <span className="page-header-meta">
          <Badge status={pool.owner_tenant_id ? "info" : "neutral"} label={pool.owner_tenant_id ? "BYO" : "platform-managed"} icon="shield" />
        </span>
      </div>

      <div className="action-bar">
        <Button size="sm" onClick={() => { setKeyIds(members.map((m) => m.key_id).join(",")); setEditing(true); }}>
          Edit members
        </Button>
        <Button size="sm" variant="danger" onClick={() => setConfirmDelete(true)}>Delete pool</Button>
      </div>

      <StrategyForm pool={pool} />

      <div className="section">
        <div className="section-title">Members ({members.length})</div>
        {members.length === 0 ? (
          <EmptyState variant="zero-data" title="No members in this pool" />
        ) : (
          <table className="p-table">
            <thead>
              <tr><th scope="col">Label</th><th scope="col">Status</th><th scope="col">Weight</th><th scope="col">Success</th><th scope="col">Latency</th><th scope="col">Credits</th></tr>
            </thead>
            <tbody>
              {members.map((m) => (
                <tr key={m.key_id}>
                  <td>{m.label}</td>
                  <td>{m.status}</td>
                  <td>{m.weight ?? "—"}</td>
                  <td>{formatPercent(m.success_ewma)}</td>
                  <td>{formatLatencyMs(m.latency_ewma_ms)}</td>
                  <td>{formatCount(m.credits_remaining)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="two-col">
        <SelectionStateView poolId={id} />
        <SimulatePanel poolId={id} />
      </div>

      <Modal
        open={editing}
        onClose={() => setEditing(false)}
        title="Replace members"
        busy={putMembers.isPending}
        footer={
          <>
            <Button onClick={() => setEditing(false)}>Cancel</Button>
            <Button variant="primary" loading={putMembers.isPending}
              onClick={() =>
                putMembers.mutate(keyIds.split(",").map((x) => x.trim()).filter(Boolean), {
                  onSuccess: () => { toast.success("Members replaced"); setEditing(false); },
                })
              }>
              Save members
            </Button>
          </>
        }
      >
        <Input label="Key ids (csv) — full replacement" value={keyIds} onChange={setKeyIds} mono
          description="PUT /key-pools/{id}/members replaces the whole set." />
      </Modal>

      <ConfirmDialog
        open={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        onConfirm={() => {
          setConfirmDelete(false);
          del.mutate(undefined, { onSuccess: () => { toast.success("Pool deleted"); navigate("/key-pools"); } });
        }}
        title="Delete this pool?"
        body="Members are unaffected. Rejected with 409 conflict if an active routing policy references it."
        confirmLabel="Delete"
        danger
        busy={del.isPending}
      />
    </>
  );
}
