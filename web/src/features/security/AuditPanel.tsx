// features/security/AuditPanel.tsx — AUDIT LOG + chain verify + change-history (doc 09 §11.1).
// The hash-chain verify status is a first-class badge: VERIFIED with a timestamp, or a CRITICAL
// persistent banner naming the first bad seq + the runbook (doc 04 §2.12, doc 14). A verify
// mismatch is never a toast (doc 09 §11.3). Row expand shows before/after jsonb in a CodeBlock.
import { Fragment, useState } from "react";
import { isApiError } from "../../api/client";
import { Badge, Button, CodeBlock, EmptyState, Input } from "../../design/primitives";
import { formatUtc } from "../../lib/format";
import { useAuditLog, useAuditVerify, useChangeHistory } from "./api";
import type { AuditFilters } from "./api";

function VerifyBadge() {
  const verify = useAuditVerify();
  if (verify.data && !verify.data.ok) {
    return (
      <div role="alert" style={{ border: "1px solid var(--status-error)", background: "var(--status-error-bg)", borderRadius: "var(--radius-1)", padding: "var(--space-3)" }}>
        <Badge status="error" icon="triangle" label={`chain MISMATCH at seq ${verify.data.first_bad_seq ?? "?"}`} />
        <p style={{ margin: "var(--space-2) 0 0" }}>
          The audit hash chain failed verification. Follow the runbook <strong>audit-chain mismatch</strong> (doc 14).
        </p>
      </div>
    );
  }
  return (
    <div style={{ display: "flex", gap: "var(--space-2)", alignItems: "center" }}>
      {verify.data?.ok ? (
        <Badge status="ok" icon="check" label={`VERIFIED ${verify.data.verified_at ? formatUtc(verify.data.verified_at) : ""}`} />
      ) : (
        <Badge status="neutral" icon="shield" label="chain not verified this session" />
      )}
      <Button size="sm" loading={verify.isFetching} onClick={() => void verify.refetch()}>
        Verify now
      </Button>
    </div>
  );
}

function ChangeHistoryViewer() {
  const [kind, setKind] = useState("provider");
  const [id, setId] = useState("");
  const [active, setActive] = useState<{ kind: string; id: string } | null>(null);
  const history = useChangeHistory(active?.kind ?? "", active?.id ?? "", active !== null);

  return (
    <section style={{ marginTop: "var(--space-6)" }}>
      <h3>Change history</h3>
      <div style={{ display: "flex", gap: "var(--space-3)", alignItems: "flex-end", flexWrap: "wrap" }}>
        <Input label="Object kind" value={kind} onChange={setKind} />
        <Input label="Object id" value={id} onChange={setId} />
        <Button onClick={() => id && setActive({ kind, id })} disabled={!id}>
          Load timeline
        </Button>
      </div>
      {active ? (
        history.isPending ? (
          <div className="skeleton" style={{ height: 120, marginTop: "var(--space-3)" }} aria-busy="true" />
        ) : history.isError ? (
          <EmptyState variant="error" title="Could not load change history" errorCode={isApiError(history.error) ? history.error.code : undefined} />
        ) : (
          <ul style={{ marginTop: "var(--space-3)" }}>
            {(history.data?.items ?? []).map((e, i) => (
              <li key={i}>
                <strong>{formatUtc(e.at)}</strong> · {e.kind} — {e.summary}
                {e.actor ? ` (${e.actor})` : ""}
              </li>
            ))}
          </ul>
        )
      ) : null}
    </section>
  );
}

export function AuditPanel() {
  const [filters, setFilters] = useState<AuditFilters>({});
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const audit = useAuditLog(filters);
  const items = audit.data?.items ?? [];

  const setF = (k: keyof AuditFilters, v: string) => setFilters((f) => ({ ...f, [k]: v || undefined }));
  function toggle(seq: number) {
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(seq)) n.delete(seq);
      else n.add(seq);
      return n;
    });
  }

  return (
    <section>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: "var(--space-3)", flexWrap: "wrap", marginBottom: "var(--space-3)" }}>
        <h2>Audit log</h2>
        <VerifyBadge />
      </div>

      <div style={{ display: "flex", gap: "var(--space-3)", flexWrap: "wrap", marginBottom: "var(--space-3)" }}>
        <Input label="Actor user id" value={filters.actor_user_id ?? ""} onChange={(v) => setF("actor_user_id", v)} />
        <Input label="Action" value={filters.action ?? ""} onChange={(v) => setF("action", v)} />
        <Input label="Object kind" value={filters.object_kind ?? ""} onChange={(v) => setF("object_kind", v)} />
      </div>

      {audit.isError ? (
        <EmptyState variant="error" title="Could not load audit log" errorCode={isApiError(audit.error) ? audit.error.code : undefined} action={{ label: "Retry", onClick: () => void audit.refetch() }} />
      ) : audit.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" aria-label="Loading audit log" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-results" title="No audit rows match the filters" />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">seq</th>
              <th scope="col">actor</th>
              <th scope="col">action</th>
              <th scope="col">object</th>
              <th scope="col">ip</th>
              <th scope="col">at</th>
            </tr>
          </thead>
          <tbody>
            {items.map((r) => (
              <Fragment key={r.seq}>
                <tr onClick={() => toggle(r.seq)} style={{ cursor: "pointer" }} aria-expanded={expanded.has(r.seq)}>
                  <td data-mono>{r.seq}</td>
                  <td>{r.actor ?? r.actor_user_id ?? "—"}</td>
                  <td>{r.action}</td>
                  <td>{r.object_kind}/{r.object_id}</td>
                  <td>{r.ip ?? "—"}</td>
                  <td>{formatUtc(r.at)}</td>
                </tr>
                {expanded.has(r.seq) ? (
                  <tr>
                    <td colSpan={6}>
                      <CodeBlock language="json" code={JSON.stringify({ before: r.before ?? null, after: r.after ?? null }, null, 2)} />
                    </td>
                  </tr>
                ) : null}
              </Fragment>
            ))}
          </tbody>
        </table>
      )}

      <ChangeHistoryViewer />
    </section>
  );
}
