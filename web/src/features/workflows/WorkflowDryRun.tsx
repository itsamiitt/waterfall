// features/workflows/WorkflowDryRun.tsx — the Dry-Run panel (doc 09 §7.1, doc 07 §7). Renders
// the planned Provider order + expected cost/Confidence per Field, the projected stop, and the
// PROVENANCE of inherited values (resolver output). Zero egress is a backend guarantee, surfaced
// here. Pure presentational (no router/query hooks) so it render-tests under react-dom/server —
// the P10 "dry-run render" gate.
import { Badge } from "../../design/primitives";
import type { DryRunResult } from "./types";

export function WorkflowDryRun({ result }: { result: DryRunResult | null }) {
  if (!result) {
    return (
      <p className="wf-muted">
        Run a dry-run to preview the plan — no Provider calls are made (zero egress, G3).
      </p>
    );
  }
  return (
    <div className="wf-dryrun">
      <p className="wf-dryrun-egress">
        <Badge status="ok" label="zero egress" icon="shield" />
        {result.zero_egress
          ? "The Planner is pure — no provider.Call on this path (backend guarantee)."
          : "warning: zero_egress flag not asserted by the server"}
      </p>

      {Object.entries(result.by_field).map(([field, steps]) => (
        <div key={field} className="wf-dryrun-field">
          <span className="wf-dryrun-field-name">{field}</span>
          <ol className="wf-dryrun-order">
            {steps.map((s, i) => (
              <li key={s.provider}>
                <span className="wf-dryrun-rank">{i + 1}</span>
                <span className="wf-provider">{s.provider}</span>
                <span className="wf-dryrun-metrics">
                  {s.cost_credits}cr @ {s.expected_confidence.toFixed(2)}
                </span>
              </li>
            ))}
          </ol>
        </div>
      ))}

      <dl className="wf-dryrun-summary">
        <div>
          <dt>expected_total_cost_credits</dt>
          <dd>{result.max_committed_credits}</dd>
        </div>
        <div>
          <dt>projected stop</dt>
          <dd>
            {result.stop_projection.condition} · ≈{result.stop_projection.expected_providers_used} providers{" "}
            <span className="wf-muted">(modeled)</span>
          </dd>
        </div>
      </dl>

      {result.diff_vs_active ? (
        <p className="wf-muted">
          diff_vs_active: order {result.diff_vs_active.provider_order_changed ? "changed" : "unchanged"}
          {result.diff_vs_active.added.length ? `, +${result.diff_vs_active.added.join(", ")}` : ""}
          {result.diff_vs_active.removed.length ? `, -${result.diff_vs_active.removed.join(", ")}` : ""}
        </p>
      ) : null}

      <div className="wf-provenance">
        <span className="wf-provenance-title">Provenance of inherited values (resolver, not client-derived):</span>
        <span className="wf-muted">levels consulted: {result.resolved_scope.levels_consulted.join(" → ")}</span>
        <ul>
          {Object.entries(result.resolved_scope.overrides).map(([provider, o]) => (
            <li key={provider}>
              <strong>{provider}</strong>: {o.effective} — from {o.source}
              {o.source_version !== undefined ? `, v${o.source_version}` : ""}
            </li>
          ))}
        </ul>
      </div>

      {result.warnings.length > 0 ? (
        <ul className="wf-dryrun-warnings">
          {result.warnings.map((w) => (
            <li key={`${w.rule}${w.path}`}>
              <Badge status="warn" label={w.rule} icon="flag" /> {w.message}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
