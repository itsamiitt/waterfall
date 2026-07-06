// features/security/SettingsPage.tsx — Settings › IP allowlist (doc 09 §11.2, doc 04 §2.2).
// Full-replacement PUT; empty list disables enforcement. Lockout guard: if the caller's current
// IP falls outside the new set the server returns 422 validation_failed — shown verbatim.
import { useEffect, useState } from "react";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Button, EmptyState, Input } from "../../design/primitives";
import { useIpAllowlists, useUpdateIpAllowlists } from "./api";

export default function SettingsPage() {
  const allowlists = useIpAllowlists();
  const update = useUpdateIpAllowlists();
  const [entries, setEntries] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [err, setErr] = useState<string | undefined>();

  useEffect(() => {
    if (allowlists.data) setEntries(allowlists.data.entries);
  }, [allowlists.data]);

  function add() {
    const v = draft.trim();
    if (!v) return;
    setEntries((e) => (e.includes(v) ? e : [...e, v]));
    setDraft("");
  }

  function save() {
    setErr(undefined);
    update.mutate(entries, {
      onSuccess: () => toast.success("IP allowlist replaced"),
      onError: (e) =>
        setErr(isApiError(e) ? e.message : "Save failed — the current set is unchanged"),
    });
  }

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Settings</h1>
        <span className="page-header-meta">IP allowlist (CIDR)</span>
      </div>

      {allowlists.isError ? (
        <EmptyState variant="error" title="Could not load the IP allowlist" errorCode={isApiError(allowlists.error) ? allowlists.error.code : undefined} action={{ label: "Retry", onClick: () => void allowlists.refetch() }} />
      ) : allowlists.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" aria-label="Loading settings" />
      ) : (
        <div style={{ maxWidth: 520, display: "flex", flexDirection: "column", gap: "var(--space-3)" }}>
          <p style={{ color: "var(--color-text-muted)", fontSize: "var(--text-sm)" }}>
            An empty list disables allowlist enforcement. Your current IP must remain inside the new set,
            or the server rejects the change (422) to prevent lockout.
          </p>
          <ul style={{ display: "flex", flexDirection: "column", gap: "var(--space-2)" }}>
            {entries.length === 0 ? <li style={{ color: "var(--color-text-muted)" }}>No entries — enforcement disabled.</li> : null}
            {entries.map((e) => (
              <li key={e} style={{ display: "flex", gap: "var(--space-2)", alignItems: "center" }}>
                <code style={{ fontFamily: "var(--font-mono)" }}>{e}</code>
                <Button size="sm" variant="ghost" onClick={() => setEntries((x) => x.filter((y) => y !== e))} aria-label={`Remove ${e}`}>
                  remove
                </Button>
              </li>
            ))}
          </ul>
          <div style={{ display: "flex", gap: "var(--space-2)", alignItems: "flex-end" }}>
            <Input label="Add CIDR" value={draft} onChange={setDraft} mono description="e.g. 203.0.113.0/24" />
            <Button onClick={add}>Add</Button>
          </div>
          {err ? <p role="alert" style={{ color: "var(--status-error)" }}>{err}</p> : null}
          <div>
            <Button variant="primary" onClick={save} loading={update.isPending}>
              Save allowlist (full replacement)
            </Button>
          </div>
        </div>
      )}
    </>
  );
}
