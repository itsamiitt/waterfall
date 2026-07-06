// features/approvals — Approvals inbox (doc 09 §11.1). Pending list + selected request detail;
// live via approval.request.changed (SSE `approval` topic). Decisions go through the MFA step-up.
import { useState } from "react";
import { isApiError } from "../../api/client";
import { useSseTopics } from "../../api/sse";
import { Badge, Button, EmptyState } from "../../design/primitives";
import { approvalStatusInfo, type ApprovalStatus } from "../../lib/status";
import { formatUtc, relativeTime } from "../../lib/format";
import { useApprovals } from "./api";
import { ApprovalDetail } from "./ApprovalDetail";

export default function ApprovalsPage() {
  useSseTopics(["approval"]);
  const approvals = useApprovals({ status: "pending" });
  const [selected, setSelected] = useState<string | null>(null);

  const items = approvals.data?.items ?? [];

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Approvals</h1>
        <span className="page-header-meta">{items.length} pending</span>
      </div>

      {approvals.isError ? (
        <EmptyState
          variant="error"
          title="Could not load approvals"
          errorCode={isApiError(approvals.error) ? approvals.error.code : undefined}
          action={{ label: "Retry", onClick: () => void approvals.refetch() }}
        />
      ) : approvals.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" aria-label="Loading approvals" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-data" title="No approval requests — gated actions will appear here" />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">kind</th>
              <th scope="col">requested by</th>
              <th scope="col">expires</th>
              <th scope="col">quorum</th>
              <th scope="col">status</th>
              <th scope="col">actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((a) => {
              const st = approvalStatusInfo(a.status as ApprovalStatus);
              return (
                <tr key={a.id} aria-selected={selected === a.id}>
                  <td>{a.action_kind}</td>
                  <td>{a.requested_by ?? a.requested_by_user_id ?? "—"}</td>
                  <td title={formatUtc(a.expires_at)}>{relativeTime(a.expires_at) === "—" ? formatUtc(a.expires_at) : `in ${relativeTime(a.expires_at).replace(" ago", "")}`}</td>
                  <td>
                    {a.decisions.length}/{a.required_approvals}
                  </td>
                  <td>
                    <Badge status={st.token} icon={st.icon} label={st.label} />
                  </td>
                  <td>
                    <Button size="sm" onClick={() => setSelected(a.id)}>
                      Review
                    </Button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      {selected ? (
        <div style={{ marginTop: "var(--space-5)" }}>
          <ApprovalDetail id={selected} />
        </div>
      ) : null}
    </>
  );
}
