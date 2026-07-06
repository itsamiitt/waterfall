// features/approvals/ApprovalDetail.tsx — the REVIEW panel (doc 09 §11.1). Shows the pinned
// payload, the diff, the validation_report and the dry-run artifacts, decisions + quorum + expiry,
// and approve/reject behind the MFA step-up dialog. Four-eyes: the requester cannot approve.
import { useState } from "react";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Badge, Button, CodeBlock, EmptyState } from "../../design/primitives";
import { approvalStatusInfo, type ApprovalStatus } from "../../lib/status";
import { formatUtc } from "../../lib/format";
import { useApproval, useApprove, useCancel, useReject } from "./api";
import { StepUpModal } from "./StepUpModal";

function Artifact({ title, value }: { title: string; value: unknown }) {
  if (value === undefined || value === null) return null;
  return (
    <section style={{ marginTop: "var(--space-4)" }}>
      <h3>{title}</h3>
      <CodeBlock language="json" code={typeof value === "string" ? value : JSON.stringify(value, null, 2)} />
    </section>
  );
}

export function ApprovalDetail({ id }: { id: string }) {
  const detail = useApproval(id);
  const approve = useApprove(id);
  const reject = useReject(id);
  const cancel = useCancel(id);
  const [dialog, setDialog] = useState<"Approve" | "Reject" | null>(null);
  const [error, setError] = useState<string | undefined>();

  if (detail.isPending) return <div className="skeleton" style={{ height: 320 }} aria-busy="true" aria-label="Loading request" />;
  if (detail.isError)
    return (
      <EmptyState
        variant="error"
        title="Could not load the request"
        errorCode={isApiError(detail.error) ? detail.error.code : undefined}
        action={{ label: "Retry", onClick: () => void detail.refetch() }}
      />
    );

  const r = detail.data;
  const st = approvalStatusInfo(r.status as ApprovalStatus);
  const busy = approve.isPending || reject.isPending;

  function submit(code: string, comment: string) {
    setError(undefined);
    const mut = dialog === "Reject" ? reject : approve;
    mut.mutate(
      { comment, mfaCode: code },
      {
        onSuccess: () => {
          toast.success(`Request ${dialog === "Reject" ? "rejected" : "approved"}`);
          setDialog(null);
        },
        onError: (e) => {
          if (!isApiError(e)) return setError("Decision failed — retry");
          if (e.code === "mfa_required") return setError("MFA code missing or invalid — re-enter your TOTP code");
          if (e.code === "forbidden") return setError("four-eyes: requester cannot approve own request");
          if (e.code === "conflict") return setError("This request is expired — the decision cannot be recorded");
          setError(e.message);
        },
      },
    );
  }

  const isPending = r.status === "pending";

  return (
    <section style={{ border: "1px solid var(--color-border)", borderRadius: "var(--radius-1)", padding: "var(--space-4)" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", flexWrap: "wrap", gap: "var(--space-2)" }}>
        <h2 style={{ margin: 0 }}>
          Review: {r.action_kind} <Badge status={st.token} icon={st.icon} label={st.label} />
        </h2>
        <span style={{ color: "var(--color-text-muted)" }}>
          quorum {r.decisions.length}/{r.required_approvals} · expires {formatUtc(r.expires_at)}
        </span>
      </div>

      <p style={{ color: "var(--color-text-muted)" }}>
        requested by {r.requested_by ?? r.requested_by_user_id ?? "—"} · four-eyes: the requester cannot approve.
      </p>

      <Artifact title="Pinned payload" value={r.payload} />
      <Artifact title="Diff" value={r.diff} />
      <Artifact title="Validation report" value={r.validation_report} />
      <Artifact title="Dry-run artifacts" value={r.dry_run} />
      <Artifact title="Execution result" value={r.execution_result} />

      <section style={{ marginTop: "var(--space-4)" }}>
        <h3>Decisions</h3>
        {r.decisions.length === 0 ? (
          <p style={{ color: "var(--color-text-muted)" }}>none yet</p>
        ) : (
          <ul>
            {r.decisions.map((d, i) => (
              <li key={i}>
                <strong>{d.decision}</strong> by {d.approver_email ?? d.approver_user_id} · mfa {d.mfa_verified ? "✓" : "✗"} · {formatUtc(d.created_at)}
                {d.comment ? ` — "${d.comment}"` : ""}
              </li>
            ))}
          </ul>
        )}
      </section>

      {isPending ? (
        <div style={{ display: "flex", gap: "var(--space-2)", marginTop: "var(--space-4)" }}>
          <Button variant="danger" onClick={() => { setError(undefined); setDialog("Reject"); }}>
            Reject
          </Button>
          <Button variant="primary" onClick={() => { setError(undefined); setDialog("Approve"); }}>
            Approve
          </Button>
          <Button onClick={() => cancel.mutate(undefined, { onError: () => toast.error("Cancel failed") })} loading={cancel.isPending}>
            Cancel request
          </Button>
        </div>
      ) : null}

      <StepUpModal
        open={dialog !== null}
        action={dialog ?? "Approve"}
        busy={busy}
        error={error}
        onClose={() => setDialog(null)}
        onSubmit={submit}
      />
    </section>
  );
}
