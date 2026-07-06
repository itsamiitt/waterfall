// features/rotation — Key Pools list (doc 09 §4.2), grouped by ownership (platform vs BYO
// owner_tenant_id). Screen → endpoint: GET /key-pools; POST /key-pools (create). Row →
// /key-pools/:id.
import { useState } from "react";
import { Link } from "react-router";
import { Button, EmptyState, Input, Modal, Badge } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { usePools, useCreatePool } from "./api";
import type { KeyPool } from "./types";

function PoolTable({ title, pools }: { title: string; pools: KeyPool[] }) {
  if (pools.length === 0) return null;
  return (
    <div className="section">
      <div className="section-title">{title}</div>
      <table className="p-table">
        <thead>
          <tr><th scope="col">Selector</th><th scope="col">Strategy</th><th scope="col">Members</th><th scope="col">Status</th></tr>
        </thead>
        <tbody>
          {pools.map((p) => (
            <tr key={p.id}>
              <td><Link to={`/key-pools/${encodeURIComponent(p.id)}`}>{p.selector}</Link></td>
              <td><Badge status="info" label={p.strategy} icon="refresh" /></td>
              <td>{p.member_count ?? "—"}</td>
              <td>{p.status ?? "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function CreatePoolModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const create = useCreatePool();
  const [f, setF] = useState({ provider_id: "", name: "" });
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Create key pool"
      busy={create.isPending}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={create.isPending} disabled={!f.provider_id || !f.name}
            onClick={() =>
              create.mutate(
                { provider_id: f.provider_id, name: f.name },
                { onSuccess: () => { toast.success("Pool created"); onClose(); setF({ provider_id: "", name: "" }); } },
              )
            }>
            Create
          </Button>
        </>
      }
    >
      <Input label="Provider id" value={f.provider_id} onChange={(v) => setF((s) => ({ ...s, provider_id: v }))} mono
        description="Selector is provider_id:name (matches AuthDescriptor.KeyPoolSelector)." />
      <Input label="Pool name" value={f.name} onChange={(v) => setF((s) => ({ ...s, name: v }))} mono />
    </Modal>
  );
}

export default function PoolsList() {
  const q = usePools({});
  const [creating, setCreating] = useState(false);
  const pools = q.data ?? [];
  const platform = pools.filter((p) => !p.owner_tenant_id);
  const byo = pools.filter((p) => !!p.owner_tenant_id);

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Key pools</h1>
      </div>
      <div className="action-bar">
        <Button size="sm" variant="primary" onClick={() => setCreating(true)}>+ Create pool</Button>
        <Link to="/rotation"><Button size="sm">Rotation engine</Button></Link>
      </div>

      {q.isError ? (
        <EmptyState variant="error" title="Could not load key pools"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }} />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 240 }} aria-busy="true" />
      ) : pools.length === 0 ? (
        <EmptyState variant="zero-data" title="No key pools" action={{ label: "Create pool", onClick: () => setCreating(true) }} />
      ) : (
        <>
          <PoolTable title="Platform-managed" pools={platform} />
          <PoolTable title="Bring-your-own (tenant)" pools={byo} />
        </>
      )}

      <CreatePoolModal open={creating} onClose={() => setCreating(false)} />
    </>
  );
}
