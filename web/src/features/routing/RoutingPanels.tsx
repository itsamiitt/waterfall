// features/routing/RoutingPanels.tsx — side + lower panels for the routing editor (doc 09 §6.1):
// validation report (inline, field-anchored — NOT toasts, doc 09 §6.3), draft-vs-active diff,
// dry-run result (zero egress + provenance), version rail (rollback = publish of a prior
// version), and the 202-approval banner.
import { Badge, Button, CodeBlock } from "../../design/primitives";
import { configVersionStatusInfo } from "../../lib/status";
import { formatUtc } from "../../lib/format";
import { describeEffective } from "./lifecycle";
import type {
  ConfigVersion,
  DryRunResult,
  RoutingPolicyPayload,
  ValidationEntry,
  ValidationReport,
} from "./types";

// ---- validation report ----

export function ValidationPanel({ report }: { report: ValidationReport | null | undefined }) {
  if (!report) {
    return <p className="rt-muted">Not validated yet. Run Validate — the server validator is authority.</p>;
  }
  const { errors, warnings } = report;
  if (errors.length === 0 && warnings.length === 0) {
    return (
      <p className="rt-ok">
        <Badge status="ok" label="Validated" icon="check" /> No errors or warnings. Publish is enabled.
      </p>
    );
  }
  return (
    <ul className="rt-report">
      {errors.map((e) => (
        <ReportRow key={`${e.rule}${e.path}`} entry={e} />
      ))}
      {warnings.map((w) => (
        <ReportRow key={`${w.rule}${w.path}`} entry={w} />
      ))}
    </ul>
  );
}

function ReportRow({ entry }: { entry: ValidationEntry }) {
  const isErr = entry.severity === "error";
  return (
    <li className="rt-report-row" data-severity={entry.severity}>
      <Badge
        status={isErr ? "error" : "warn"}
        label={entry.rule}
        icon={isErr ? "triangle" : "flag"}
      />
      <span className="rt-report-msg">
        <code className="rt-path">{entry.path}</code> {entry.message}
      </span>
    </li>
  );
}

// ---- draft vs active diff ----

export function DiffView({
  draft,
  active,
  dryRunDiff,
}: {
  draft: RoutingPolicyPayload;
  active: RoutingPolicyPayload | null | undefined;
  dryRunDiff?: DryRunResult["diff_vs_active"];
}) {
  const draftOrder = draft.waterfall?.order ?? [];
  const activeOrder = active?.waterfall?.order ?? [];
  const orderChanged = JSON.stringify(draftOrder) !== JSON.stringify(activeOrder);
  return (
    <div className="rt-diff">
      <div className="rt-diff-row">
        <span className="rt-diff-label">waterfall_order</span>
        <span className="rt-diff-val" data-changed={orderChanged || undefined}>
          [{activeOrder.join(", ")}] → [{draftOrder.join(", ")}]
        </span>
      </div>
      <ThresholdDiff label="confidence_target" a={active?.thresholds?.confidence_target} b={draft.thresholds?.confidence_target} />
      <ThresholdDiff label="min_confidence" a={active?.thresholds?.min_confidence} b={draft.thresholds?.min_confidence} />
      <ThresholdDiff
        label="max_cost_credits_per_record"
        a={active?.thresholds?.max_cost_credits_per_record}
        b={draft.thresholds?.max_cost_credits_per_record}
      />
      {dryRunDiff ? (
        <p className="rt-muted">
          Server diff: order {dryRunDiff.provider_order_changed ? "changed" : "unchanged"}
          {dryRunDiff.added.length ? `, +${dryRunDiff.added.join(", ")}` : ""}
          {dryRunDiff.removed.length ? `, -${dryRunDiff.removed.join(", ")}` : ""}
        </p>
      ) : null}
    </div>
  );
}

function ThresholdDiff({ label, a, b }: { label: string; a?: number; b?: number }) {
  if (a === undefined && b === undefined) return null;
  const changed = a !== b;
  return (
    <div className="rt-diff-row">
      <span className="rt-diff-label">{label}</span>
      <span className="rt-diff-val" data-changed={changed || undefined}>
        {a ?? "—"} → {b ?? "—"}
      </span>
    </div>
  );
}

// ---- dry-run panel (zero egress + provenance) ----

export function DryRunPanel({ result }: { result: DryRunResult | null }) {
  if (!result) {
    return (
      <p className="rt-muted">
        Run a dry-run to preview the plan — no Provider calls are made (G3 zero egress).
      </p>
    );
  }
  return (
    <div className="rt-dryrun">
      <p className="rt-dryrun-egress">
        <Badge status="ok" label="zero egress" icon="shield" />
        {result.zero_egress ? "No Provider calls — backend guarantee (G3)." : "egress flag missing"}
      </p>
      {Object.entries(result.by_field).map(([field, steps]) => (
        <div key={field} className="rt-dryrun-field">
          <span className="rt-dryrun-field-name">{field}</span>
          <ol className="rt-dryrun-steps">
            {steps.map((s) => (
              <li key={s.provider}>
                {s.provider} · {s.cost_credits}cr @ {s.expected_confidence.toFixed(2)}
              </li>
            ))}
          </ol>
        </div>
      ))}
      <dl className="rt-dryrun-summary">
        <div>
          <dt>max_committed_credits</dt>
          <dd>{result.max_committed_credits}</dd>
        </div>
        <div>
          <dt>projected stop</dt>
          <dd>
            {result.stop_projection.condition} (≈{result.stop_projection.expected_providers_used} providers, modeled)
          </dd>
        </div>
      </dl>
      <div className="rt-provenance">
        <span className="rt-provenance-title">Provenance (resolver — never client-derived):</span>
        <span className="rt-muted">consulted {result.resolved_scope.levels_consulted.join(" → ")}</span>
        <ul>
          {Object.entries(result.resolved_scope.overrides).map(([provider, o]) => (
            <li key={provider}>
              <strong>{provider}</strong>: {describeEffective(o)}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

// ---- version rail ----

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
    <ol className="rt-rail" aria-label="Version history">
      {versions.map((v) => {
        const info = configVersionStatusInfo(v.status);
        const isActive = v.version === activeVersion;
        return (
          <li key={v.id} className="rt-rail-item" data-current={v.id === currentId || undefined}>
            <button type="button" className="rt-rail-open" onClick={() => onOpen(v)}>
              <span className="rt-rail-ver">v{v.version}</span>
              <Badge status={info.token} label={info.label} icon={info.icon} />
              {isActive ? <Badge status="ok" label="active" icon="flag" /> : null}
              {v.created_at ? <span className="rt-muted">{formatUtc(v.created_at)}</span> : null}
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

// ---- 202 approval banner ----

export function ApprovalBanner({ requestId }: { requestId: string }) {
  return (
    <div className="rt-approval" role="status">
      <Badge status="warn" label="Pending approval" icon="clock" />
      <span>
        Publish requires approval. Request <code>{requestId}</code> is queued — track it in{" "}
        <a href="/approvals">Approvals</a>. The pointer flips once quorum approves.
      </span>
    </div>
  );
}

export function PayloadPreview({ payload }: { payload: RoutingPolicyPayload }) {
  return <CodeBlock code={JSON.stringify(payload, null, 2)} language="json" copyable />;
}
