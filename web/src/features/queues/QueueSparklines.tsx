// features/queues/QueueSparklines.tsx — paired enq/deq series for the queue console (doc 09 §8.1),
// rendered with the shared recharts wrapper. Default export so QueueConsole can React.lazy it,
// keeping recharts out of the queue-list render path (doc 08 §10).
import { TimeSeries, type TimeSeriesPoint } from "../../lib/charts";
import type { QueueStatsPoint } from "./types";

export default function QueueSparklines({ points }: { points: QueueStatsPoint[] }) {
  const data: TimeSeriesPoint[] = points.map((p) => ({ ts: p.ts, enq: p.enq, deq: p.deq }));
  return (
    <TimeSeries
      data={data}
      series={[
        { key: "enq", label: "enqueued/s" },
        { key: "deq", label: "dequeued/s" },
      ]}
      height={200}
    />
  );
}
