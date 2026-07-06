// features/providers — lifecycle action bar (doc 09 §2.1/§2.2). Screen → endpoint:
// POST /providers/{id}/{enable|disable|pause|maintenance|test|health-check|refresh-metadata|
// sync-credits|benchmark|duplicate|archive} and DELETE /providers/{id}. Errors surface through
// the global MutationCache toast ({code}: {message}, doc 08 §8). archive + delete are
// approval-gated → the 202 {approval_request_id} pending banner (doc 09 §2.3, P9 acceptance #2);
// benchmark → 202 {job_id}; duplicate → 201 navigates to the new draft.
import { useState } from "react";
import { useNavigate } from "react-router";
import { Button, ConfirmDialog } from "../../design/primitives";
import { toast } from "../../app/toast";
import { useDeleteProvider, useProviderAction, type ProviderAction } from "./api";
import type { Accepted } from "../../api/types";
import type { Provider } from "./types";

const isAccepted = (d: unknown): d is Accepted =>
  typeof d === "object" && d !== null && ("approval_request_id" in d || "job_id" in d);

interface Pending {
  action: ProviderAction | "delete";
  title: string;
  body: string;
  danger?: boolean;
}

const CONFIRMS: Record<string, Omit<Pending, "action">> = {
  test: {
    title: "Run smoke test?",
    body: "Spends real credits on one live descriptor call (G3-bounded, G4-capped).",
  },
  benchmark: {
    title: "Run benchmark?",
    body: "Runs a fixed sample through the real adapter — spends real credits (G3/G4-bounded). Returns a job you can track.",
  },
  archive: {
    title: "Archive this provider?",
    body: "Approval-gated: submitting creates a pending approval request; history is preserved.",
  },
  delete: {
    title: "Delete this provider?",
    body: "Approval-gated and irreversible once approved. Submitting creates a pending approval request.",
    danger: true,
  },
};

const QUICK: { action: ProviderAction; label: string }[] = [
  { action: "enable", label: "Enable" },
  { action: "disable", label: "Disable" },
  { action: "pause", label: "Pause" },
  { action: "maintenance", label: "Maintenance" },
  { action: "health-check", label: "Health check" },
  { action: "sync-credits", label: "Sync credits" },
  { action: "refresh-metadata", label: "Refresh metadata" },
  { action: "duplicate", label: "Duplicate" },
];

export function ProviderActions({ provider }: { provider: Provider }) {
  const navigate = useNavigate();
  const act = useProviderAction(provider.id);
  const del = useDeleteProvider(provider.id);
  const [pending, setPending] = useState<Pending | null>(null);
  const [approvalId, setApprovalId] = useState<string | null>(null);

  function onResult(a: ProviderAction | "delete", data: Provider | Accepted) {
    if (isAccepted(data)) {
      if ("approval_request_id" in data) setApprovalId(data.approval_request_id);
      else toast.success(`Benchmark started (job ${data.job_id.slice(0, 8)})`);
      return;
    }
    if (a === "duplicate") {
      navigate(`/providers/${encodeURIComponent(data.id)}/config`);
      return;
    }
    toast.success(`Provider ${a.replace(/-/g, " ")}`);
  }

  function run(a: ProviderAction) {
    act.mutate(a, { onSuccess: (data) => onResult(a, data) });
  }

  function confirmAndRun() {
    if (!pending) return;
    const a = pending.action;
    setPending(null);
    if (a === "delete") {
      del.mutate(undefined, { onSuccess: (data) => onResult("delete", data) });
    } else {
      run(a);
    }
  }

  const gated = (a: ProviderAction | "delete") => a in CONFIRMS;

  return (
    <>
      {approvalId ? (
        <div className="banner" role="status">
          <strong>Approval requested.</strong> Pending approval request{" "}
          <code>{approvalId.slice(0, 12)}</code>. Track it in{" "}
          <a href="/approvals">Approvals</a>.
        </div>
      ) : null}

      <div className="action-bar">
        {QUICK.map((q) => (
          <Button key={q.action} size="sm" onClick={() => run(q.action)} loading={act.isPending}>
            {q.label}
          </Button>
        ))}
        {(["test", "benchmark", "archive"] as ProviderAction[]).map((a) => (
          <Button
            key={a}
            size="sm"
            onClick={() => setPending({ action: a, ...CONFIRMS[a]! })}
          >
            {a[0]!.toUpperCase() + a.slice(1)}
          </Button>
        ))}
        <Button size="sm" variant="danger" onClick={() => setPending({ action: "delete", ...CONFIRMS.delete! })}>
          Delete
        </Button>
      </div>

      <ConfirmDialog
        open={pending !== null}
        onClose={() => setPending(null)}
        onConfirm={confirmAndRun}
        title={pending?.title ?? ""}
        body={pending?.body}
        confirmLabel={pending && gated(pending.action) && pending.action === "delete" ? "Request delete" : "Confirm"}
        danger={pending?.danger}
        busy={act.isPending || del.isPending}
      />
    </>
  );
}
