// features/queues/QueuesPage.tsx — /queues. Live queue cards from GET /queues, patched by
// queue.stats.tick (replace-snapshot). Links to the per-queue console and the DLQ (doc 09 §8).
import { Link } from "react-router";
import { isApiError } from "../../api/client";
import { EmptyState } from "../../design/primitives";
import { RequireRole } from "../../app/guards";
import { useSseStatus, useSseTopics } from "../../api/sse";
import { formatUtcTime } from "../../lib/format";
import { QueueCard } from "./QueueCard";
import { useQueues } from "./api";

export function QueuesPage() {
  useSseTopics(["queue"]);
  const sse = useSseStatus();
  const q = useQueues();

  const degraded = sse === "degraded" || sse === "reconnecting";

  return (
    <RequireRole group="queues.fleet.read">
      <div className="page-header">
        <h1>Queues</h1>
        <span className="page-header-meta">
          {q.data?.generated_at ? `generated_at ${formatUtcTime(q.data.generated_at)}` : ""}
          {degraded ? ` — stream ${sse}` : ""}
        </span>
        <Link to="/dead-letters" className="qu-dlq-link">
          Dead letters →
        </Link>
      </div>

      {q.isPending ? (
        <div className="qu-grid" aria-busy="true">
          {Array.from({ length: 4 }, (_, i) => (
            <div key={i} className="skeleton" style={{ height: 220 }} />
          ))}
        </div>
      ) : q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load queues"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.data.queues.length === 0 ? (
        <EmptyState variant="zero-data" title="No queues registered" body="Queue definitions arrive with the engine deployment." />
      ) : (
        <div className="qu-grid">
          {q.data.queues.map((queue) => (
            <QueueCard key={queue.name} queue={queue} />
          ))}
        </div>
      )}
    </RequireRole>
  );
}
