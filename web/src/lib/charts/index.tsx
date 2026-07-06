// lib/charts — recharts wrappers (doc 08 §2/§6.1). P8 ships the seam + one wrapper so the
// pinned dependency compiles; P11 adds StackedBars, Histogram, UptimeBar, Heatmap. Charts
// consume tokens only (no hardcoded hex): series colors come from the CSS custom properties
// below, and every series is also distinguishable by dash pattern (doc 08 §9).
//
// IMPORTANT: features must import this module ONLY via dynamic import / lazy routes —
// recharts is excluded from the initial chunk by construction (doc 08 §10).
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

/** Ordered, color-blind-safe series palette (tokens; validated with the status palette). */
const SERIES_TOKENS = [
  "var(--color-accent)",
  "var(--status-warn)",
  "var(--status-paused)",
  "var(--status-ok)",
  "var(--status-error)",
] as const;

const SERIES_DASHES = ["", "6 3", "2 2", "8 4 2 4", "1 3"] as const;

export interface TimeSeriesPoint {
  /** UTC bucket timestamp (doc 04 §1.8). */
  ts: string;
  [series: string]: string | number | null | undefined;
}

export interface TimeSeriesProps {
  data: readonly TimeSeriesPoint[];
  series: readonly { key: string; label: string }[];
  height?: number;
  yTickFormatter?: (v: number) => string;
}

export function TimeSeries({ data, series, height = 240, yTickFormatter }: TimeSeriesProps) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <LineChart data={data as TimeSeriesPoint[]} margin={{ top: 8, right: 8, bottom: 4, left: 4 }}>
        <CartesianGrid stroke="var(--color-border)" strokeDasharray="2 4" vertical={false} />
        <XAxis
          dataKey="ts"
          stroke="var(--color-text-faint)"
          tick={{ fill: "var(--color-text-muted)", fontSize: 12 }}
          tickFormatter={(v: string) => v.slice(11, 16)}
        />
        <YAxis
          stroke="var(--color-text-faint)"
          tick={{ fill: "var(--color-text-muted)", fontSize: 12 }}
          tickFormatter={yTickFormatter}
          width={48}
        />
        <Tooltip
          contentStyle={{
            background: "var(--color-bg-raised)",
            border: "1px solid var(--color-border)",
            borderRadius: "var(--radius-1)",
            color: "var(--color-text)",
          }}
        />
        {series.map((s, i) => (
          <Line
            key={s.key}
            type="monotone"
            dataKey={s.key}
            name={s.label}
            stroke={SERIES_TOKENS[i % SERIES_TOKENS.length]}
            strokeDasharray={SERIES_DASHES[i % SERIES_DASHES.length] || undefined}
            dot={false}
            isAnimationActive={false}
          />
        ))}
      </LineChart>
    </ResponsiveContainer>
  );
}
