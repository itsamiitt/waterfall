// / — Global Overview (doc 09 §1): StatTile grid bound to GET /v1/admin/overview, live-
// patched by overview.tiles.tick (replace-snapshot QoS). No time-range control here by
// design; drill-downs own history. 19 skeleton tiles on first load; error variant carries
// the envelope code + Retry; degraded SSE shows the snapshot age (doc 09 §1.4).
import type { SseStatus } from "../../api/sse";
import { useSseStatus, useSseTopics } from "../../api/sse";
import { isApiError } from "../../api/client";
import { Badge, EmptyState, StatTile } from "../../design/primitives";
import type { StatusDescriptor } from "../../lib/status";
import { formatUtcTime, relativeTime } from "../../lib/format";
import { useOverview } from "./api";
import { orderedTiles } from "./tiles";

/** Live connection indicator (doc 09 §1.4): color + icon + label, never color-only. */
export function connectionDescriptor(status: SseStatus): StatusDescriptor {
  switch (status) {
    case "live":
      return { token: "ok", icon: "check", label: "Live" };
    case "connecting":
      return { token: "info", icon: "refresh", label: "Connecting" };
    case "reconnecting":
      return { token: "warn", icon: "refresh", label: "Reconnecting" };
    case "degraded":
      return { token: "error", icon: "triangle", label: "Degraded" };
    default:
      return { token: "neutral", icon: "dot", label: "Idle" };
  }
}

export default function OverviewPage() {
  useSseTopics(["overview", "alert"]);
  const sseStatus = useSseStatus();
  const snapshot = useOverview();

  if (snapshot.isPending) {
    return (
      <>
        <div className="page-header">
          <h1>Global overview</h1>
        </div>
        <div className="tile-grid" aria-busy="true" aria-label="Loading tiles">
          {Array.from({ length: 19 }, (_, i) => (
            <div key={i} className="skeleton" style={{ height: 108 }} />
          ))}
        </div>
      </>
    );
  }

  if (snapshot.isError) {
    return (
      <>
        <div className="page-header">
          <h1>Global overview</h1>
        </div>
        <EmptyState
          variant="error"
          title="Could not load the overview snapshot"
          errorCode={isApiError(snapshot.error) ? snapshot.error.code : undefined}
          body={snapshot.error instanceof Error ? snapshot.error.message : undefined}
          action={{ label: "Retry", onClick: () => void snapshot.refetch() }}
        />
      </>
    );
  }

  const { generated_at, tiles } = snapshot.data;
  const degraded = sseStatus === "degraded" || sseStatus === "reconnecting";
  const conn = connectionDescriptor(sseStatus);

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Global overview</h1>
        <span className="page-header-meta">
          <Badge status={conn.token} icon={conn.icon} label={conn.label} />
          {" generated_at "}
          {formatUtcTime(generated_at)}
          {degraded ? ` (${relativeTime(generated_at)} old)` : ""}
        </span>
      </div>
      <div className="tile-grid">
        {orderedTiles(tiles).map(({ meta, value }) => (
          <StatTile
            key={meta.id}
            label={meta.label}
            value={meta.render(value)}
            unit={meta.unit}
            delta={typeof value.delta_pct === "number" ? value.delta_pct : undefined}
            href={meta.href}
            sub={meta.sub?.(value)}
            freshness={degraded ? "stale" : "live"}
          />
        ))}
      </div>
    </>
  );
}
