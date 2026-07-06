// features/security/UsersPanel.tsx — USERS CRUD (doc 09 §11.1). Invite, edit role/status, reset
// password (invalidates the User's sessions). Last-tenant_admin self-demotion → 409 conflict is
// surfaced inline (doc 04 §2.2).
import { useState } from "react";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Badge, Button, EmptyState, Input, Modal, Select, type SelectOption } from "../../design/primitives";
import { formatUtc } from "../../lib/format";
import type { Role } from "../../lib/permissions";
import { useCreateUser, useDeleteUser, useResetPassword, useUpdateUser, useUsers } from "./api";
import type { AdminUser } from "../../api/types";

const roleOpts: SelectOption<Role>[] = [
  { value: "operator", label: "operator" },
  { value: "tenant_admin", label: "tenant_admin" },
  { value: "tenant_user", label: "tenant_user" },
];
const statusOpts: SelectOption[] = [
  { value: "active", label: "active" },
  { value: "disabled", label: "disabled" },
];

export function UsersPanel() {
  const users = useUsers({});
  const create = useCreateUser();
  const update = useUpdateUser();
  const del = useDeleteUser();
  const reset = useResetPassword();

  const [inviting, setInviting] = useState(false);
  const [invite, setInvite] = useState<{ email: string; role: Role }>({ email: "", role: "tenant_user" });
  const [editing, setEditing] = useState<AdminUser | null>(null);
  const [editErr, setEditErr] = useState<string | undefined>();

  const items = users.data?.items ?? [];

  function submitInvite() {
    create.mutate(invite, {
      onSuccess: () => {
        toast.success("User invited");
        setInviting(false);
        setInvite({ email: "", role: "tenant_user" });
      },
      onError: (e) => toast.error(isApiError(e) ? `Invite failed (${e.code})` : "Invite failed"),
    });
  }

  function saveEdit() {
    if (!editing) return;
    setEditErr(undefined);
    update.mutate(
      { id: editing.id, body: { role: editing.role, status: editing.status } },
      {
        onSuccess: () => {
          toast.success("User updated");
          setEditing(null);
        },
        onError: (e) =>
          setEditErr(
            isApiError(e) && e.code === "conflict"
              ? "Cannot demote the last tenant_admin (409 conflict)"
              : isApiError(e)
                ? e.message
                : "Update failed",
          ),
      },
    );
  }

  return (
    <section>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "var(--space-3)" }}>
        <h2>Users</h2>
        <Button size="sm" variant="primary" onClick={() => setInviting(true)}>
          + Invite user
        </Button>
      </div>

      {users.isError ? (
        <EmptyState
          variant="error"
          title="Could not load users"
          errorCode={isApiError(users.error) ? users.error.code : undefined}
          action={{ label: "Retry", onClick: () => void users.refetch() }}
        />
      ) : users.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" aria-label="Loading users" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-data" title="No users yet" />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">email</th>
              <th scope="col">role</th>
              <th scope="col">status</th>
              <th scope="col">mfa</th>
              <th scope="col">created</th>
              <th scope="col">actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((u) => (
              <tr key={u.id}>
                <td>{u.email}</td>
                <td>{u.role}</td>
                <td>
                  {u.status === "active" ? (
                    <Badge status="ok" icon="check" label="active" />
                  ) : (
                    <Badge status="neutral" icon="slash" label="disabled" />
                  )}
                </td>
                <td>
                  {u.mfa_enrolled ? (
                    <Badge status="ok" icon="shield" label="enrolled" />
                  ) : (
                    <Badge status="warn" icon="triangle" label="not enrolled" />
                  )}
                </td>
                <td>{formatUtc(u.created_at)}</td>
                <td style={{ display: "flex", gap: "var(--space-2)" }}>
                  <Button size="sm" onClick={() => { setEditErr(undefined); setEditing(u); }}>
                    edit
                  </Button>
                  <Button
                    size="sm"
                    loading={reset.isPending && reset.variables === u.id}
                    onClick={() =>
                      reset.mutate(u.id, {
                        onSuccess: () => toast.success("Reset issued — the User's sessions are invalidated"),
                        onError: () => toast.error("Reset failed"),
                      })
                    }
                  >
                    reset
                  </Button>
                  <Button
                    size="sm"
                    variant="danger"
                    onClick={() =>
                      del.mutate(u.id, { onError: (e) => toast.error(isApiError(e) ? e.code : "Delete failed") })
                    }
                  >
                    disable
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Modal open={inviting} onClose={() => setInviting(false)} title="Invite user" busy={create.isPending}
        footer={
          <>
            <Button onClick={() => setInviting(false)} disabled={create.isPending}>Cancel</Button>
            <Button variant="primary" onClick={submitInvite} loading={create.isPending} disabled={!invite.email}>Invite</Button>
          </>
        }>
        <Input label="Email" value={invite.email} onChange={(v) => setInvite({ ...invite, email: v })} required />
        <Select label="Role" options={roleOpts} value={invite.role} onChange={(v) => setInvite({ ...invite, role: v })} />
      </Modal>

      <Modal open={editing !== null} onClose={() => setEditing(null)} title="Edit user" busy={update.isPending}
        footer={
          <>
            <Button onClick={() => setEditing(null)} disabled={update.isPending}>Cancel</Button>
            <Button variant="primary" onClick={saveEdit} loading={update.isPending}>Save</Button>
          </>
        }>
        {editing ? (
          <>
            <p>{editing.email}</p>
            <Select label="Role" options={roleOpts} value={editing.role} onChange={(v) => setEditing({ ...editing, role: v })} />
            <Select label="Status" options={statusOpts} value={editing.status} onChange={(v) => setEditing({ ...editing, status: v as AdminUser["status"] })} />
            {editErr ? <p role="alert" style={{ color: "var(--status-error)" }}>{editErr}</p> : null}
          </>
        ) : null}
      </Modal>
    </section>
  );
}
