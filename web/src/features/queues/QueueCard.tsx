// features/queues/QueueCard.tsx — one queue's state-count vector + rate comparison + oldest-age
// lead tile (doc 09 §8.1). ACCUMULATING when enq>deq; a queue with depth but zero live workers
// gets the prominent warning panel (doc 09 §8.3), not an empty state. Presentational.
import { Link } from "react-router";
import { Badge, StatTile } from "../../design/primitives";
import { formatCount, formatDurationS } from "../../lib/format";
import type { QueueState, QueueSummary } from "./types";

const STATES: QueueState[] = ["waiting", "running", "scheduled", "delayed", "retry", "failed", "dead"];
const ALERT_STATES = new Set<QueueState>(["retry", "failed", "dead"]);

function depth(q: QueueSummary): number {
  return STATES.reduce((sum, s) => sum + (q[s] ?? 0), 0);
}

export function QueueCard({ queue }: { queue: QueueSummary }) {
  const hasDepth = depth(queue) > 0;
  const zeroWorkers = (queue.live_workers ?? 0) === 0;
  return (
    <section className="qu-card">
      <div className="qu-card-head">
        <Link to={`/queues/${encodeURIComponent(queue.name)}`} className="qu-card-name">
          {queue.name}
        </Link>
        {queue.accumulating ? <Badge status="warn" label="ACCUMULATING" icon="triangle" /> : null}
      </div>

      <dl className="qu-vector">
        {STATES.map((s) => (
          <div key={s} className="qu-vector-cell" data-alert={(ALERT_STATES.has(s) && queue[s] > 0) || undefined}>
            <dt>{s}</dt>
            <dd>{formatCount(queue[s])}</dd>
          </div>
        ))}
      </dl>

      <div className="qu-card-foot">
        <StatTile label="oldest age" value={formatDurationS(queue.oldest_age_s)} />
        <span className="qu-rates">
          enq {formatCount(queue.enq_rate)}/s{" "}
          <span aria-hidden="true">{queue.accumulating ? "»" : "~"}</span> deq {formatCount(queue.deq_rate)}/s
        </span>
      </div>

      {hasDepth && zeroWorkers ? (
        <p className="qu-zero-workers" role="alert">
          <Badge status="error" label="0 live workers" icon="triangle" /> This queue has depth but no
          worker is claiming — jobs will not drain. See{" "}
          <Link to="/workers">Workers</Link>.
        </p>
      ) : null}
    </section>
  );
}
