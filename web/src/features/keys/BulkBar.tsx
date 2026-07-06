// features/keys — bulk action bar with the select-all-matching-filter escalation (doc 09 §3.1,
// P9 acceptance #2). "N selected on page" escalates to "all M matching filter"; a filter-scoped
// op sends the FILTER PREDICATE (buildBulkRequest, mode:"filter") — never an id list. Screen →
// endpoint: POST /keys/bulk (202 {job_id}); op=delete is approval-gated → 202
// {approval_request_id} pending banner. M is the preview count (doc 04 §4.2).
import { useState } from "react";
import { Button, ConfirmDialog } from "../../design/primitives";
import { formatCount } from "../../lib/format";
import { toast } from "../../app/toast";
import { buildBulkRequest, isApprovalGatedOp, resolveScope } from "./bulkFilter";
import { useBulk } from "./api";
import type { BulkOp, KeyFilter } from "./types";

const OPS: { op: BulkOp; label: string; danger?: boolean }[] = [
  { op: "enable", label: "Enable" },
  { op: "disable", label: "Disable" },
  { op: "pause", label: "Pause" },
  { op: "rotate", label: "Rotate" },
  { op: "delete", label: "Delete", danger: true },
];

export interface BulkBarProps {
  providerId: string;
  filter: KeyFilter;
  selection: ReadonlySet<string>;
  serverTotal?: number;
  allMatching: boolean;
  onSelectAllMatching: () => void;
  onClearSelection: () => void;
  onJob: (jobId: string) => void;
}

export function BulkBar(props: BulkBarProps) {
  const { providerId, filter, selection, serverTotal, allMatching } = props;
  const bulk = useBulk();
  const [pendingOp, setPendingOp] = useState<BulkOp | null>(null);
  const [approvalId, setApprovalId] = useState<string | null>(null);

  const pageCount = selection.size;
  const targetCount = allMatching ? (serverTotal ?? 0) : pageCount;
  if (pageCount === 0 && !allMatching && !approvalId) return null;

  function submit() {
    if (!pendingOp) return;
    const op = pendingOp;
    setPendingOp(null);
    const scope = resolveScope(selection, { allMatching });
    const req = buildBulkRequest(providerId, filter, scope, op, { reason: `bulk ${op} via console` });
    bulk.mutate(req, {
      onSuccess: (data) => {
        if ("approval_request_id" in data) {
          setApprovalId(data.approval_request_id);
        } else {
          toast.success(`Bulk ${op} accepted (job ${data.job_id.slice(0, 8)})`);
          props.onJob(data.job_id);
          props.onClearSelection();
        }
      },
    });
  }

  return (
    <>
      {approvalId ? (
        <div className="banner" role="status">
          <strong>Approval requested.</strong> Bulk delete is gated — pending request{" "}
          <code>{approvalId.slice(0, 12)}</code>. Track it in <a href="/approvals">Approvals</a>.
        </div>
      ) : null}

      <div className="bulk-bar" role="region" aria-label="Bulk actions">
        <span className="bulk-summary">
          {allMatching ? (
            <>All <strong>{formatCount(serverTotal)}</strong> matching filter selected</>
          ) : (
            <>
              <strong>{formatCount(pageCount)}</strong> selected on page
              {serverTotal !== undefined && serverTotal > pageCount ? (
                <>
                  {" · "}
                  <button className="link-btn" onClick={props.onSelectAllMatching}>
                    Select all {formatCount(serverTotal)} matching filter
                  </button>
                </>
              ) : null}
            </>
          )}
          {(allMatching || pageCount > 0) ? (
            <button className="link-btn" onClick={props.onClearSelection}> · Clear</button>
          ) : null}
        </span>
        <span className="bulk-ops">
          {OPS.map((o) => (
            <Button
              key={o.op}
              size="sm"
              variant={o.danger ? "danger" : "secondary"}
              disabled={targetCount === 0}
              onClick={() => setPendingOp(o.op)}
            >
              {o.label}
            </Button>
          ))}
        </span>
      </div>

      <ConfirmDialog
        open={pendingOp !== null}
        onClose={() => setPendingOp(null)}
        onConfirm={submit}
        title={pendingOp ? `Bulk ${pendingOp}` : ""}
        body={
          pendingOp
            ? `${pendingOp} ${formatCount(targetCount)} key(s)${
                allMatching ? " matching the current filter (re-evaluated under RLS at execution)" : ""
              }.`
            : undefined
        }
        consequences={
          pendingOp && isApprovalGatedOp(pendingOp)
            ? ["Delete is irreversible once approved and routes through the approvals gate."]
            : undefined
        }
        danger={pendingOp === "delete"}
        requireTypedPhrase={pendingOp === "delete" ? "delete" : undefined}
        confirmLabel={pendingOp && isApprovalGatedOp(pendingOp) ? "Request delete" : "Confirm"}
        busy={bulk.isPending}
      />
    </>
  );
}
