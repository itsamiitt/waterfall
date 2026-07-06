// features/security/SessionsPanel.tsx — SESSIONS (doc 09 §11.1). List + revoke; revoking your
// own current session logs you out (doc 04 §2.1) — flagged in the confirm copy.
import { useState } from "react";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Badge, Button, ConfirmDialog, EmptyState } from "../../design/primitives";
import { formatUtc, relativeTime } from "../../lib/format";
import { useRevokeSession, useSessions } from "./api";
import type { SessionRow } from "./types";

export function SessionsPanel() {
  const sessions = useSessions();
  const revoke = useRevokeSession();
  const [target, setTarget] = useState<SessionRow | null>(null);

  const items = sessions.data?.items ?? [];

  function confirmRevoke() {
    if (!target) return;
    revoke.mutate(target.id, {
      onSuccess: () => {
        toast.success("Session revoked");
        setTarget(null);
      },
      onError: (e) => toast.error(isApiError(e) ? e.code : "Revoke failed"),
    });
  }

  return (
    <section>
      <h2>Sessions</h2>
      {sessions.isError ? (
        <EmptyState
          variant="error"
          title="Could not load sessions"
          errorCode={isApiError(sessions.error) ? sessions.error.code : undefined}
          action={{ label: "Retry", onClick: () => void sessions.refetch() }}
        />
      ) : sessions.isPending ? (
        <div className="skeleton" style={{ height: 160 }} aria-busy="true" aria-label="Loading sessions" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-data" title="No active sessions" />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">id</th>
              <th scope="col">user</th>
              <th scope="col">ip</th>
              <th scope="col">user agent</th>
              <th scope="col">created</th>
              <th scope="col">last seen</th>
              <th scope="col">actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((s) => (
              <tr key={s.id}>
                <td data-mono>
                  {s.id.slice(0, 8)}…
                  {s.is_current ? <Badge status="info" icon="dot" label="this session" /> : null}
                </td>
                <td>{s.user_email ?? s.user_id}</td>
                <td>{s.ip ?? "—"}</td>
                <td>{s.user_agent ?? "—"}</td>
                <td>{formatUtc(s.created_at)}</td>
                <td>{s.last_seen_at ? relativeTime(s.last_seen_at) : "—"}</td>
                <td>
                  <Button size="sm" variant="danger" onClick={() => setTarget(s)}>
                    revoke
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <ConfirmDialog
        open={target !== null}
        onClose={() => setTarget(null)}
        onConfirm={confirmRevoke}
        title="Revoke session"
        body={target?.is_current ? "This is your current session — revoking it will log you out." : "Revoke this session?"}
        consequences={target ? [`session ${target.id.slice(0, 8)}… for ${target.user_email ?? target.user_id}`] : []}
        confirmLabel="Revoke"
        danger
        busy={revoke.isPending}
      />
    </section>
  );
}
