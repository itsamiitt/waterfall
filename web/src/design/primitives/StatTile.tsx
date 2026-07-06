import type { ReactNode } from "react";
import { Link } from "react-router";

export interface StatTileProps {
  label: string;
  value: ReactNode;
  unit?: string;
  /** Signed delta in percent units (e.g. +4.2); rendered with direction color + sign. */
  delta?: number;
  spark?: readonly number[];
  /** Drill-down deep link (doc 09 §1.2 map). */
  href?: string;
  freshness?: "live" | "stale";
  sub?: ReactNode;
}

function Sparkline({ points }: { points: readonly number[] }) {
  if (points.length < 2) return null;
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const step = 100 / (points.length - 1);
  const path = points
    .map((p, i) => `${i === 0 ? "M" : "L"}${(i * step).toFixed(1)},${(28 - ((p - min) / span) * 24 + 2).toFixed(1)}`)
    .join(" ");
  return (
    <svg className="p-stattile-spark" viewBox="0 0 100 32" width="100" height="32" aria-hidden="true">
      <path d={path} fill="none" stroke="currentColor" strokeWidth="2" />
    </svg>
  );
}

export function StatTile({ label, value, unit, delta, spark, href, freshness, sub }: StatTileProps) {
  const body = (
    <>
      <span className="p-stattile-label">{label}</span>
      <span className="p-stattile-value">
        {value}
        {unit ? <span className="p-stattile-unit">{unit}</span> : null}
      </span>
      {delta !== undefined && !Number.isNaN(delta) ? (
        <span
          className="p-stattile-delta"
          data-direction={delta > 0 ? "up" : delta < 0 ? "down" : undefined}
        >
          {delta > 0 ? "+" : ""}
          {delta}% vs yesterday
        </span>
      ) : null}
      {sub ? <span className="p-stattile-delta">{sub}</span> : null}
      {spark ? <Sparkline points={spark} /> : null}
      {freshness === "stale" ? (
        <span className="p-stattile-freshness" data-freshness="stale">
          may be stale
        </span>
      ) : null}
    </>
  );
  if (href) {
    return (
      <Link className="p-stattile" to={href}>
        {body}
      </Link>
    );
  }
  return <div className="p-stattile">{body}</div>;
}
