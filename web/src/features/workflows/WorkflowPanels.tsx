// features/workflows/WorkflowPanels.tsx — validation report (field-anchored), version rail
// (rollback = publish of a prior version), and the 202-approval banner (doc 09 §7).
import { Badge, Button } from "../../design/primitives";
import { configVersionStatusInfo } from "../../lib/status";
import { formatUtc } from "../../lib/format";
import type { ConfigVersion, ValidationEntry, ValidationReport } from "./types";

export function ValidationPanel({ report }: { report: ValidationReport | null | undefined }) {
  if (!report) {
    return <p className="wf-muted">Not validated yet. The server validator is authority.</p>;
  }
  if (report.errors.length === 0 && report.warnings.length === 0) {
    return (
      <p className="wf-ok">
        <Badge status="ok" label="Validated" icon="check" /> No errors or warnings — publish is enabled.
      </p>
    );
  }
  return (
    <ul className="wf-report">
      {[...report.errors, ...report.warnings].map((e: ValidationEntry) => (
        <li key={`${e.rule}${e.path}`} className="wf-report-row" data-severity={e.severity}>
          <Badge status={e.severity === "error" ? "error" : "warn"} label={e.rule} icon={e.severity === "error" ? "triangle" : "flag"} />
          <span>
            <code className="wf-path">{e.path}</code> {e.message}
          </span>
        </li>
      ))}
    </ul>
  );
}

export function VersionRail({
  versions,
  activeVersion,
  currentId,
  onOpen,
  onRollback,
  rollbackBusy,
}: {
  versions: ConfigVersion[];
  activeVersion: number | null;
  currentId: string | undefined;
  onOpen: (v: ConfigVersion) => void;
  onRollback: (v: ConfigVersion) => void;
  rollbackBusy: boolean;
}) {
  return (
    <ol className="wf-rail" aria-label="Version history">
      {versions.map((v) => {
        const info = configVersionStatusInfo(v.status);
        const isActive = v.version === activeVersion;
        return (
          <li key={v.id} className="wf-rail-item" data-current={v.id === currentId || undefined}>
            <button type="button" className="wf-rail-open" onClick={() => onOpen(v)}>
              <span className="wf-rail-ver">v{v.version}</span>
              <Badge status={info.token} label={info.label} icon={info.icon} />
              {isActive ? <Badge status="ok" label="active" icon="flag" /> : null}
              {v.created_at ? <span className="wf-muted">{formatUtc(v.created_at)}</span> : null}
            </button>
            {!isActive && (v.status === "archived" || v.status === "published") ? (
              <Button size="sm" variant="secondary" loading={rollbackBusy} onClick={() => onRollback(v)}>
                Roll back to v{v.version}
              </Button>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}

export function ApprovalBanner({ requestId }: { requestId: string }) {
  return (
    <div className="wf-approval" role="status">
      <Badge status="warn" label="Pending approval" icon="clock" />
      <span>
        Publish requires approval. Request <code>{requestId}</code> is queued — track it in{" "}
        <a href="/approvals">Approvals</a>. In-flight Enrichment Jobs pin their config_version_id and are unaffected.
      </span>
    </div>
  );
}
