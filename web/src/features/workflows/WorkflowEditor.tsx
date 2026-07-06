// features/workflows/WorkflowEditor.tsx — /workflows/:scope/edit. Stepped-canvas builder with a
// node inspector and a Dry-Run panel (doc 09 §7). Publish is gated on server validate (publishGate);
// a stale publish surfaces the server 409. Rollback = publish of a prior version.
import { useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import { isApiError } from "../../api/client";
import { Badge, Button, ConfirmDialog, EmptyState } from "../../design/primitives";
import { configVersionStatusInfo } from "../../lib/status";
import { toast } from "../../app/toast";
import { RequireRole } from "../../app/guards";
import { useSseTopics } from "../../api/sse";
import { nodeErrorPaths, publishGate, reportSeverity } from "./lifecycle";
import { WorkflowCanvas } from "./WorkflowCanvas";
import { NodeInspector } from "./NodeInspector";
import { WorkflowDryRun } from "./WorkflowDryRun";
import { ApprovalBanner, ValidationPanel, VersionRail } from "./WorkflowPanels";
import {
  useClone,
  useCreateDraft,
  useDryRun,
  usePatchDraft,
  usePublish,
  useRollback,
  useValidate,
  useWorkflowVersion,
  useWorkflowVersions,
} from "./api";
import type { ConfigVersion, DryRunResult, WaterfallWorkflowPayload } from "./types";

const EMPTY_PAYLOAD: WaterfallWorkflowPayload = {
  schema_version: 1,
  name: "new-workflow",
  trigger: "api",
  fields: ["work_email"],
  entry_provider: "",
  timeout_ms: 8000,
  confidence_threshold: 0.85,
  max_cost_credits: 5,
  max_providers: 4,
  stop_conditions: ["target-met"],
};

export function WorkflowEditorPage() {
  const { scope = "default" } = useParams();
  useSseTopics(["approval"]);
  return (
    <RequireRole group="workflows.edit">
      <Editor scope={scope} />
    </RequireRole>
  );
}

function Editor({ scope }: { scope: string }) {
  const versionsQ = useWorkflowVersions(scope);
  const versions = versionsQ.data?.versions ?? [];
  const editable = versions.filter((v) => v.status === "draft" || v.status === "validated");
  const activeVersion = versions.find((v) => v.status === "published")?.version ?? null;
  const defaultId = (editable[0] ?? versions[0])?.id;
  const [selectedId, setSelectedId] = useState<string | undefined>(undefined);
  const currentId = selectedId ?? defaultId;

  const versionQ = useWorkflowVersion(scope, currentId);
  const version = versionQ.data;

  const [working, setWorking] = useState<WaterfallWorkflowPayload>(EMPTY_PAYLOAD);
  const [dirtySinceValidate, setDirty] = useState(false);
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  const [approvalRequestId, setApprovalId] = useState<string | null>(null);
  const [dryRun, setDryRunResult] = useState<DryRunResult | null>(null);
  const [confirmPublish, setConfirmPublish] = useState(false);
  const [rollbackTarget, setRollbackTarget] = useState<ConfigVersion | null>(null);
  const seededFor = useRef<string | undefined>(undefined);

  useEffect(() => {
    if (version && seededFor.current !== version.id) {
      seededFor.current = version.id;
      setWorking(version.payload ?? EMPTY_PAYLOAD);
      setDirty(false);
      setDryRunResult(null);
    }
  }, [version]);

  const createDraft = useCreateDraft(scope);
  const patchDraft = usePatchDraft(scope, currentId ?? "");
  const validate = useValidate(scope, currentId ?? "");
  const dryRunM = useDryRun(scope, currentId ?? "");
  const publish = usePublish(scope, currentId ?? "");
  const clone = useClone(scope);
  const rollback = useRollback(scope);

  const workingJson = JSON.stringify(working);
  useEffect(() => {
    if (!currentId || !version) return;
    if (version.status === "published" || version.status === "archived") return;
    if (workingJson === JSON.stringify(version.payload ?? EMPTY_PAYLOAD)) return;
    const t = setTimeout(() => patchDraft.mutate(working), 600);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workingJson, currentId, version?.status]);

  const nodeErrors = useMemo(() => nodeErrorPaths(version?.validation_report), [version?.validation_report]);

  if (versionsQ.isPending) return <EditorSkeleton scope={scope} />;
  if (versionsQ.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load workflow versions"
        errorCode={isApiError(versionsQ.error) ? versionsQ.error.code : undefined}
        action={{ label: "Retry", onClick: () => void versionsQ.refetch() }}
      />
    );
  }
  if (versions.length === 0 || !currentId) {
    return (
      <>
        <Header scope={scope} activeVersion={activeVersion} />
        <EmptyState
          variant="zero-data"
          title="No Waterfall for this scope"
          body="Create a draft to shape one Waterfall: entry, parallel prefix, sequential legs, fallback."
          action={{
            label: createDraft.isPending ? "Creating…" : "Create draft",
            onClick: () =>
              createDraft.mutate(EMPTY_PAYLOAD, {
                onSuccess: (v) => setSelectedId(v.id),
                onError: (e) => toast.error(isApiError(e) ? e.message : "create failed"),
              }),
          }}
        />
      </>
    );
  }

  const displayStatus =
    version?.status === "validated" && dirtySinceValidate ? "draft" : version?.status ?? "draft";
  const gate = publishGate(displayStatus, dirtySinceValidate, version?.validation_report?.errors.length ?? 0);
  const readOnly = displayStatus === "published" || displayStatus === "archived";
  const statusInfo = configVersionStatusInfo(displayStatus);

  function onChange(next: WaterfallWorkflowPayload) {
    setWorking(next);
    if (version?.status === "validated") setDirty(true);
  }

  function runValidate() {
    validate.mutate(undefined, {
      onSuccess: (v) => {
        setDirty(false);
        const sev = reportSeverity(v.validation_report);
        if (sev === "error") toast.error("Validation found errors — see the inline node markers");
        else if (sev === "warn") toast.info("Validated with warnings");
        else toast.success("Validated — publish is enabled");
      },
      onError: (e) => toast.error(isApiError(e) ? e.message : "validate failed"),
    });
  }

  function runDryRun() {
    dryRunM.mutate(undefined, {
      onSuccess: (r) => setDryRunResult(r),
      onError: (e) => toast.error(isApiError(e) ? e.message : "dry-run failed"),
    });
  }

  function doPublish() {
    setConfirmPublish(false);
    publish.mutate(version?.parent_version_id ?? undefined, {
      onSuccess: (r) => {
        if ("approval_request_id" in r) {
          setApprovalId(r.approval_request_id);
          toast.info("Publish submitted for approval");
        } else {
          toast.success(`Published v${r.version} (epoch ${r.epoch})`);
          void versionsQ.refetch();
        }
      },
      onError: (e) => {
        if (isApiError(e) && e.status === 409) {
          setDirty(true);
          toast.error("Draft changed since validate — re-validate before publishing");
          void versionQ.refetch();
        } else {
          toast.error(isApiError(e) ? e.message : "publish failed");
        }
      },
    });
  }

  return (
    <>
      <Header scope={scope} activeVersion={activeVersion}>
        <span className="wf-version-badge">
          v{version?.version} <Badge status={statusInfo.token} label={statusInfo.label} icon={statusInfo.icon} />
        </span>
      </Header>

      <div className="wf-toolbar">
        <Button variant="secondary" loading={validate.isPending} disabled={readOnly} onClick={runValidate}>
          Validate
        </Button>
        <Button variant="secondary" loading={dryRunM.isPending} onClick={runDryRun}>
          Dry-run
        </Button>
        <Button
          variant="primary"
          disabled={!gate.canPublish || publish.isPending}
          loading={publish.isPending}
          onClick={() => setConfirmPublish(true)}
          title={gate.reason ?? undefined}
        >
          Publish
        </Button>
        <Button variant="ghost" onClick={() => clone.mutate(currentId)} loading={clone.isPending}>
          Clone
        </Button>
        {patchDraft.isPending ? <span className="wf-muted">saving…</span> : null}
        {!gate.canPublish && gate.reason ? (
          <span className="wf-gate-reason" role="status">Publish disabled: {gate.reason}</span>
        ) : null}
      </div>

      {approvalRequestId ? <ApprovalBanner requestId={approvalRequestId} /> : null}

      <WorkflowCanvas
        payload={working}
        onChange={onChange}
        selected={selectedNode}
        onSelect={setSelectedNode}
        nodeErrors={nodeErrors}
        disabled={readOnly}
      />

      <div className="wf-lower-grid">
        <Panel title="Node inspector">
          <NodeInspector selected={selectedNode} payload={working} onChange={onChange} disabled={readOnly} />
        </Panel>
        <Panel title="Dry-run (zero egress)">
          <WorkflowDryRun result={dryRun} />
        </Panel>
        <Panel title="Validation report">
          <ValidationPanel report={version?.validation_report} />
        </Panel>
        <Panel title="Version rail">
          <VersionRail
            versions={versions}
            activeVersion={activeVersion}
            currentId={currentId}
            onOpen={(v: ConfigVersion) => setSelectedId(v.id)}
            onRollback={(v: ConfigVersion) => setRollbackTarget(v)}
            rollbackBusy={rollback.isPending}
          />
        </Panel>
      </div>

      <ConfirmDialog
        open={confirmPublish}
        onClose={() => setConfirmPublish(false)}
        onConfirm={doPublish}
        title={`Publish workflow v${version?.version}`}
        body="Publishing is approval-gated. In-flight Enrichment Jobs pin config_version_id at start, so running work is unaffected."
        consequences={[`Scope: ${scope}`, `Supersedes active v${activeVersion ?? "—"}`]}
        confirmLabel="Submit publish"
        busy={publish.isPending}
      />

      <ConfirmDialog
        open={rollbackTarget !== null}
        onClose={() => setRollbackTarget(null)}
        onConfirm={() => {
          const v = rollbackTarget;
          if (!v) return;
          rollback.mutate(v.version, {
            onSuccess: (r) => {
              setRollbackTarget(null);
              if ("approval_request_id" in r) {
                setApprovalId(r.approval_request_id);
                toast.info("Rollback submitted for approval");
              } else {
                toast.success(`Rolled back to v${r.version}`);
              }
            },
            onError: (e) => {
              setRollbackTarget(null);
              toast.error(isApiError(e) ? e.message : "rollback failed");
            },
          });
        }}
        title={`Roll back to v${rollbackTarget?.version}`}
        body="Rollback IS a publish of the prior version: same approval gate, validators re-run against the current world."
        consequences={[`Re-publishes v${rollbackTarget?.version}`, "Approval-gated", "Nothing is destroyed"]}
        confirmLabel="Roll back"
        busy={rollback.isPending}
      />
    </>
  );
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="wf-panel">
      <h2 className="wf-panel-title">{title}</h2>
      {children}
    </section>
  );
}

function Header({
  scope,
  activeVersion,
  children,
}: {
  scope: string;
  activeVersion: number | null;
  children?: React.ReactNode;
}) {
  return (
    <div className="page-header wf-header">
      <h1>Waterfall · {scope}</h1>
      <span className="page-header-meta">active v{activeVersion ?? "—"}</span>
      {children}
    </div>
  );
}

function EditorSkeleton({ scope }: { scope: string }) {
  return (
    <>
      <div className="page-header">
        <h1>Waterfall · {scope}</h1>
      </div>
      <div className="skeleton" style={{ height: 160 }} aria-busy="true" />
      <div className="wf-lower-grid" aria-busy="true">
        <div className="skeleton" style={{ height: 240 }} />
        <div className="skeleton" style={{ height: 240 }} />
      </div>
    </>
  );
}
