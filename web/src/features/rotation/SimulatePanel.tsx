// features/rotation — simulate panel (doc 09 §4.1). Screen → endpoint:
// POST /key-pools/{id}/simulate {draws} → per-Key selection distribution, ZERO egress (doc 04
// §2.5). Empty state before first run explains the panel (doc 09 §4.3).
import { useState } from "react";
import { Button, EmptyState, Input } from "../../design/primitives";
import { formatPercent } from "../../lib/format";
import { useSimulate } from "./api";

export function SimulatePanel({ poolId }: { poolId: string }) {
  const sim = useSimulate(poolId);
  const [draws, setDraws] = useState("1000");

  return (
    <div className="section">
      <div className="section-title">Simulate</div>
      <div className="filter-bar">
        <Input label="Draws" value={draws} onChange={setDraws} inputMode="numeric" />
        <Button size="sm" variant="primary" loading={sim.isPending} onClick={() => sim.mutate(Number(draws) || 0)}>
          Run simulate
        </Button>
      </div>
      {sim.isIdle ? (
        <EmptyState variant="zero-data" title="Run a simulation to preview selection distribution" body="Zero provider calls are made." />
      ) : sim.isPending ? (
        <div className="skeleton" style={{ height: 160 }} aria-busy="true" />
      ) : sim.data ? (
        <div className="dist">
          {sim.data.distribution.map((d) => (
            <div key={d.key_id} className="dist-row">
              <span className="dist-label">{d.label ?? d.key_id}</span>
              <span className="dist-bar" aria-hidden="true">
                <span style={{ width: `${Math.max(0, Math.min(100, d.pct))}%` }} />
              </span>
              <span className="dist-pct">{formatPercent(d.pct, { fromPercent: true })}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
