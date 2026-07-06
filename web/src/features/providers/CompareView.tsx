// features/providers — compare view (doc 09 §2.1). Screen → endpoint:
// GET /providers/compare?ids= (declared vs measured per Field, ≤10) + GET /providers/rankings.
import { useMemo } from "react";
import { useSearchParams } from "react-router";
import { EmptyState, Input } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { formatLatencyMs, formatPercent } from "../../lib/format";
import { useCompare, useRankings } from "./api";
import type { CompareCell } from "./types";

function cellText(c: CompareCell | undefined): string {
  if (!c) return "—";
  const declared = c.declared_cost_credits !== undefined ? `${c.declared_cost_credits}cr @${c.declared_confidence ?? "?"}` : "—";
  const measured = c.measured_hit_rate !== undefined
    ? `hit ${formatPercent(c.measured_hit_rate)} · ${formatLatencyMs(c.measured_p95_ms)} · ${c.measured_cost_per_hit ?? "?"}cr/hit`
    : "no data";
  return `${declared} → ${measured}`;
}

export default function CompareView() {
  const [params, setParams] = useSearchParams();
  const idsCsv = params.get("ids") ?? "";
  const ids = useMemo(() => idsCsv.split(",").map((s) => s.trim()).filter(Boolean).slice(0, 10), [idsCsv]);
  const q = useCompare(ids);
  const rankings = useRankings();

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Compare providers</h1>
      </div>

      <div className="filter-bar">
        <Input
          label="Provider ids (csv, ≤10)"
          value={idsCsv}
          mono
          onChange={(v) => setParams((p) => { if (v) p.set("ids", v); else p.delete("ids"); return p; })}
        />
      </div>

      {ids.length === 0 ? (
        <EmptyState variant="zero-data" title="Enter provider ids to compare" body="e.g. hunter,prospeo,dropcontact" />
      ) : q.isError ? (
        <EmptyState variant="error" title="Could not load comparison"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }} />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 240 }} aria-busy="true" />
      ) : (
        <div className="p-table-wrap">
          <table className="p-table compare-heat">
            <thead>
              <tr>
                <th scope="col">Field</th>
                {q.data.providers.map((p) => (
                  <th key={p.provider_id} scope="col">{p.provider_id}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {q.data.fields.map((field) => (
                <tr key={field}>
                  <td>{field}</td>
                  {q.data.providers.map((p) => (
                    <td key={p.provider_id} data-heat>
                      {cellText(p.cells.find((c) => c.field === field))}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="section">
        <div className="section-title">Cost-per-hit ranking</div>
        {rankings.isPending ? (
          <div className="skeleton" style={{ height: 120 }} aria-busy="true" />
        ) : rankings.data ? (
          <table className="p-table">
            <thead><tr><th scope="col">Field</th><th scope="col">Provider</th><th scope="col">cr/hit</th></tr></thead>
            <tbody>
              {rankings.data.items.map((r, i) => (
                <tr key={i}><td>{r.field}</td><td>{r.provider_id}</td><td>{r.cost_per_hit}</td></tr>
              ))}
            </tbody>
          </table>
        ) : null}
      </div>
    </>
  );
}
