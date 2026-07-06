// features/providers — history tab (doc 09 §2.2). Screen → endpoint:
// GET /change-history/provider/{id} — Stripe-style event timeline (config versions + approvals
// + audit rows).
import { EmptyState } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { formatUtc } from "../../lib/format";
import { flattenPages } from "../../lib/cursors";
import { useProviderHistory } from "./api";

export function HistoryTab({ id }: { id: string }) {
  const q = useProviderHistory(id);
  const events = flattenPages(q.data ? [q.data] : undefined);

  if (q.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load history"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }}
      />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 200 }} aria-busy="true" />;
  if (events.length === 0) return <EmptyState variant="zero-data" title="No change history yet" />;

  return (
    <ol className="history-timeline">
      {events.map((e) => (
        <li key={e.id} className="history-row">
          <span className="history-when">{formatUtc(e.at)}</span>
          <span className="history-kind">{e.kind}</span>
          <span className="history-summary">
            {e.summary}
            {e.actor ? <span className="history-actor"> — {e.actor}</span> : null}
          </span>
        </li>
      ))}
    </ol>
  );
}
