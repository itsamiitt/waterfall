// features/health — the 90-day uptime strip (doc 09 §5.1). An SVG of day-segments colored by
// status token; `no_data` gets a distinct hatched treatment so it never reads as `up`
// (doc 09 §5.3). Colors come from design tokens (doc 08 §6.1) — no raw hex.
import type { StatusToken } from "../../lib/status";
import { padDays, segmentStyle, segmentTitle, type DaySegment } from "./timelineModel";

const TOKEN_VAR: Record<StatusToken, string> = {
  ok: "var(--status-ok)",
  warn: "var(--status-warn)",
  error: "var(--status-error)",
  info: "var(--status-info)",
  neutral: "var(--color-border-strong)",
  paused: "var(--status-paused)",
};

export function UptimeBar({ days, count = 90 }: { days: readonly DaySegment[]; count?: number }) {
  const segs = padDays(days, count);
  const w = 3;
  const gap = 0.6;
  const height = 26;
  return (
    <svg
      className="uptime-bar"
      viewBox={`0 0 ${segs.length * (w + gap)} ${height}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={`Uptime over the last ${count} days`}
    >
      <defs>
        <pattern id="nodata-hatch" width="3" height="3" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
          <rect width="3" height="3" fill="var(--color-bg-sunken)" />
          <line x1="0" y1="0" x2="0" y2="3" stroke="var(--color-border-strong)" strokeWidth="1" />
        </pattern>
      </defs>
      {segs.map((seg, i) => {
        const style = segmentStyle(seg.status);
        const fill = style.noData ? "url(#nodata-hatch)" : TOKEN_VAR[style.token];
        return (
          <rect key={i} x={i * (w + gap)} y={0} width={w} height={height} fill={fill} rx={0.6}>
            <title>{segmentTitle(seg)}</title>
          </rect>
        );
      })}
    </svg>
  );
}
