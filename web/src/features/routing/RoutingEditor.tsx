// features/routing/RoutingEditor.tsx — /routing/:scope/edit. Draft→validate→publish→rollback
// (doc 07 §6). The Publish button is DISABLED until POST .../validate returns ok (publishGate);
// forcing a stale publish surfaces the server's 409 version_conflict (doc 04 §2.7, P10 AC #4).
// Every lane/threshold edit debounces a PATCH and optimistically demotes validated→draft.
import { useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import { isApiError } from "../../api/client";
import { Badge, Button, ConfirmDialog, EmptyState } from "../../design/primitives";
import { configVersionStatusInfo } from "../../lib/status";
import { toast } from "../../app/toast";
import { RequireRole } from "../../app/guards";
import { useSseTopics } from "../../api/sse";
import { publishGate, reportSeverity } from "./lifecycle";
import { LanesEditor } from "./RoutingLanes";
import {
  ApprovalBanner,
  DiffView,
  DryRunPanel,
  PayloadPreview,
  ValidationPanel,
  VersionRail,
} from "./RoutingPanels";
import {
  useClone,
  useCreateDraft,
  useDryRun,
  usePatchDraft,
  usePublish,
  useRollback,
  useRoutingScopes,
  useRoutingVersion,
  useRoutingVersions,
  useValidate,
} from "./api";
import type { ConfigVersion, DryRunResult, RoutingPolicyPayload, ValidationEntry } from "./types";

const EMPTY_PAYLOAD: RoutingPolicyPayload = { schema_version: 1, waterfall: { order: [] } };

export function RoutingEditorPage() {
  const { scope = "default" } = useParams();
  useSseTopics(["approval"]);
  return (
    <RequireRole group="routing.edit">
      <Editor scope={scope} />
    </RequireRole>
  );
}

function Editor({ scope }: { scope: string }) {
  const scopesQ = useRoutingScopes();
  const versionsQ = useRoutingVersions(scope);
  const versions = versionsQ.data?.versions ?? [];

  const summary = scopesQ.data?.scopes.find((s) => s.scope_key === scope);
  const activeVersion = summary?.active_version ?? null;
  const effective = summary?.overrides ?? {};

  // Which version is open in the editor: newest editable draft/validated, else the active.
  const editable = versions.filter((v) => v.status === "draft" || v.status === "validated");
  const defaultId = (editable[0] ?? versions[0])?.id;
  const [selectedId, setSelectedId] = useState<string | undefined>(undefined);
  const currentId = selectedId ?? defaultId;

  const versionQ = useRoutingVersion(scope, currentId);
  const version = versionQ.data;

  const activeId = versions.find((v) => v.version === activeVersion)?.id;
  const activeVersionQ = useRoutingVersion(scope, activeId);

  // Working payload: seeded once per opened version id; edits stay local until the debounced PATCH.
  const [working, setWorking] = useState<RoutingPolicyPayload>(EMPTY_PAYLOAD);
  const [dirtySinceValidate, setDirty] = useState(false);
  const [approvalRequestId, setApprovalId] = useState<string | null>(null);
  const [dryRun, setDryRunResult] = useState<DryRunResult | null>(null);
  const [confirmPublish, setConfirmPublish] = useState(false);
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
  const [rollbackTarget, setRollbackTarget] = useState<ConfigVersion | null>(null);

  // Debounced PATCH on every edit (doc 04 §2.7: PATCH reverts validated→draft server-side).
  const workingJson = JSON.stringify(working);
  useEffect(() => {
    if (!currentId || !version) return;
    if (version.status === "published" || version.status === "archived") return;
    if (workingJson === JSON.stringify(version.payload ?? EMPTY_PAYLOAD)) return;
    const t = setTimeout(() => {
      patchDraft.mutate(working);
    }, 600);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workingJson, currentId, version?.status]);

  const errorsByPath = useMemo(() => {
    const map = new Map<string, ValidationEntry[]>();
    const report = version?.validation_report;
    if (report) {
      for (const e of [...report.errors, ...report.warnings]) {
        const list = map.get(e.path) ?? [];
        list.push(e);
        map.set(e.path, list);
      }
    }
    return map;
  }, [version?.validation_report]);

  if (versionsQ.isPending || scopesQ.isPending) {
    return <EditorSkeleton scope={scope} />;
  }
  if (versionsQ.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load routing versions"
        errorCode={isApiError(versionsQ.error) ? versionsQ.error.code : undefined}
        action={{ label: "Retry", onClick: () => void versionsQ.refetch() }}
      />
    );
  }
  if (versions.length === 0 || !currentId) {
    return (
      <>
        <Header scope={scope} activeVersion={activeVersion} epoch={summary?.epoch} />
        <EmptyState
          variant="zero-data"
          title="No routing policy for this scope"
          body="A new draft inherits from the next scope in precedence until published (doc 07 §3)."
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
  const gate = publishGate(
    displayStatus,
    dirtySinceValidate,
    version?.validation_report?.errors.length ?? 0,
  );
  const readOnly = displayStatus === "published" || displayStatus === "archived";
  const statusInfo = configVersionStatusInfo(displayStatus);

  function onChange(next: RoutingPolicyPayload) {
    setWorking(next);
    if (version?.status === "validated") setDirty(true);
  }

  function runValidate() {
    validate.mutate(undefined, {
      onSuccess: (v) => {
        setDirty(false);
        const sev = reportSeverity(v.validation_report);
        if (sev === "error") toast.error("Validation found errors — see the inline report");
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
      <Header scope={scope} activeVersion={activeVersion} epoch={summary?.epoch}>
        <span className="rt-version-badge">
          v{version?.version} <Badge status={statusInfo.token} label={statusInfo.label} icon={statusInfo.icon} />
        </span>
      </Header>

      <div className="rt-toolbar">
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
        {patchDraft.isPending ? <span className="rt-muted">saving…</span> : null}
        {!gate.canPublish && gate.reason ? (
          <span className="rt-gate-reason" role="status">
            Publish disabled: {gate.reason}
          </span>
        ) : null}
      </div>

      {approvalRequestId ? <ApprovalBanner requestId={approvalRequestId} /> : null}

      <div className="rt-editor-grid">
        <div className="rt-editor-main">
          <LanesEditor
            payload={working}
            effective={effective}
            onChange={onChange}
            errorsByPath={errorsByPath}
            disabled={readOnly}
          />
        </div>
        <aside className="rt-editor-side">
          <Panel title="Validation report">
            <ValidationPanel report={version?.validation_report} />
          </Panel>
          <Panel title="Thresholds">
            <ThresholdEditor payload={working} onChange={onChange} disabled={readOnly} />
          </Panel>
          <Panel title="Diff vs active">
            <DiffView draft={working} active={activeVersionQ.data?.payload} dryRunDiff={dryRun?.diff_vs_active} />
          </Panel>
          <Panel title="Dry-run (zero egress)">
            <DryRunPanel result={dryRun} />
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
          <Panel title="Draft payload">
            <PayloadPreview payload={working} />
          </Panel>
        </aside>
      </div>

      <ConfirmDialog
        open={confirmPublish}
        onClose={() => setConfirmPublish(false)}
        onConfirm={doPublish}
        title={`Publish routing v${version?.version}`}
        body="Publishing is approval-gated. The server re-checks the pinned payload_hash and the config_active pointer in one transaction; a stale draft returns 409."
        consequences={[
          `Scope: ${scope}`,
          `Supersedes active v${activeVersion ?? "—"}`,
          "Approval quorum executes against the exact hash you validated",
        ]}
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
        body="Rollback IS a publish of the prior version: same approval gate, same pointer lock. Validators re-run against the current world — a since-EXCLUDED Provider blocks it with 409."
        consequences={[`Re-publishes v${rollbackTarget?.version}`, "Approval-gated", "Nothing is destroyed"]}
        confirmLabel="Roll back"
        busy={rollback.isPending}
      />
    </>
  );
}

function ThresholdEditor({
  payload,
  onChange,
  disabled,
}: {
  payload: RoutingPolicyPayload;
  onChange: (p: RoutingPolicyPayload) => void;
  disabled?: boolean;
}) {
  const t = payload.thresholds ?? {};
  function set(key: keyof NonNullable<RoutingPolicyPayload["thresholds"]>, raw: string) {
    const v = raw === "" ? undefined : Number(raw);
    onChange({ ...payload, thresholds: { ...t, [key]: v } });
  }
  return (
    <div className="rt-thresholds">
      <NumberField label="confidence_target (0–1)" value={t.confidence_target} step={0.05} disabled={disabled} onChange={(v) => set("confidence_target", v)} />
      <NumberField label="min_confidence (0–1)" value={t.min_confidence} step={0.05} disabled={disabled} onChange={(v) => set("min_confidence", v)} />
      <NumberField label="max_cost_credits_per_record" value={t.max_cost_credits_per_record} step={1} disabled={disabled} onChange={(v) => set("max_cost_credits_per_record", v)} />
      <p className="rt-muted">max_cost cannot exceed the G4 cost ceiling — the validator (VR-7) rejects overrides.</p>
    </div>
  );
}

function NumberField({
  label,
  value,
  step,
  onChange,
  disabled,
}: {
  label: string;
  value: number | undefined;
  step: number;
  onChange: (v: string) => void;
  disabled?: boolean;
}) {
  return (
    <label className="rt-numfield">
      <span>{label}</span>
      <input
        type="number"
        className="p-input"
        step={step}
        value={value ?? ""}
        disabled={disabled}
        onChange={(e) => onChange(e.currentTarget.value)}
      />
    </label>
  );
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rt-panel">
      <h2 className="rt-panel-title">{title}</h2>
      {children}
    </section>
  );
}

function Header({
  scope,
  activeVersion,
  epoch,
  children,
}: {
  scope: string;
  activeVersion: number | null;
  epoch?: number;
  children?: React.ReactNode;
}) {
  return (
    <div className="page-header rt-header">
      <h1>Routing · {scope}</h1>
      <span className="page-header-meta">
        active v{activeVersion ?? "—"} · epoch {epoch ?? "—"}
      </span>
      {children}
    </div>
  );
}

function EditorSkeleton({ scope }: { scope: string }) {
  return (
    <>
      <div className="page-header">
        <h1>Routing · {scope}</h1>
      </div>
      <div className="rt-editor-grid" aria-busy="true">
        <div className="skeleton" style={{ height: 360 }} />
        <div className="skeleton" style={{ height: 360 }} />
      </div>
    </>
  );
}
