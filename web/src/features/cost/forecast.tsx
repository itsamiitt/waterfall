// features/cost/forecast.tsx — EOM forecast (doc 04 §2.10, doc 09 §10.1). The band is ALWAYS
// labeled modeled + indicative ~80% + UNVERIFIED; a forecast is never presented as fact. Under
// 14 days of history the endpoint returns method:"insufficient_history" → a dedicated collecting
// state (not an error).
import { Badge } from "../../design/primitives";
import { formatCompact } from "../../lib/format";
import type { CostForecast } from "./types";

const panel: React.CSSProperties = {
  border: "1px solid var(--color-border)",
  borderRadius: "var(--radius-1)",
  padding: "var(--space-4)",
  background: "var(--color-bg-raised)",
  display: "flex",
  flexDirection: "column",
  gap: "var(--space-2)",
};

export function ForecastTile({ forecast }: { forecast: CostForecast }) {
  if (forecast.method === "insufficient_history") {
    const n = forecast.days_of_history ?? 0;
    return (
      <div style={panel} aria-label="End-of-month forecast">
        <span className="p-stattile-label">EOM forecast</span>
        <span style={{ color: "var(--color-text-muted)" }}>Collecting history ({n}/14 days)</span>
        <span style={{ fontSize: "var(--text-xs)", color: "var(--color-text-faint)" }}>
          A projection needs ≥ 14 days of modeled spend before it is shown.
        </span>
      </div>
    );
  }

  return (
    <div style={panel} aria-label="End-of-month forecast">
      <span className="p-stattile-label">EOM forecast</span>
      <span className="p-stattile-value">
        {formatCompact(forecast.eom_credits)}
        <span className="p-stattile-unit">credits</span>
      </span>
      {forecast.band_low !== undefined && forecast.band_high !== undefined ? (
        <span style={{ color: "var(--color-text-muted)", fontSize: "var(--text-sm)" }}>
          ~80% band {formatCompact(forecast.band_low)} – {formatCompact(forecast.band_high)}
        </span>
      ) : null}
      <span style={{ display: "flex", gap: "var(--space-2)", flexWrap: "wrap" }}>
        <Badge status="info" icon="gauge" label="modeled" />
        <Badge status="warn" icon="triangle" label="indicative ~80% — UNVERIFIED until backtested" />
      </span>
    </div>
  );
}
