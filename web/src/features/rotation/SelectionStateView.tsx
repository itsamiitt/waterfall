// features/rotation — selection-state debug view (doc 09 §4.1). Screen → endpoint:
// GET /key-pools/{id}/selection-state. Labelled "per-instance cache — diagnostic, not truth". A
// 404 means the pool is not resident on this instance → info state, NOT an error (doc 09 §4.3).
import { EmptyState } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { useSelectionState } from "./api";

export function SelectionStateView({ poolId }: { poolId: string }) {
  const q = useSelectionState(poolId);

  if (q.isError) {
    if (isApiError(q.error) && q.error.status === 404) {
      return (
        <div className="section">
          <div className="section-title">Selection state</div>
          <EmptyState variant="zero-data" title="Pool not resident on this instance" body="Diagnostic cache is per-instance; try another replica or trigger activity." />
        </div>
      );
    }
    return (
      <EmptyState
        variant="error"
        title="Could not load selection state"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }}
      />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 140 }} aria-busy="true" />;

  const s = q.data;
  return (
    <div className="section">
      <div className="section-title">Selection state</div>
      <p className="p-field-description">per-instance cache — diagnostic, not truth</p>
      <dl className="kv-list">
        <dt>strategy</dt><dd>{s.strategy}</dd>
        <dt>ring index</dt><dd>{s.ring_index ?? "—"}</dd>
        <dt>epoch</dt><dd>{s.epoch ?? "—"}</dd>
        <dt>availability</dt><dd>{s.available ?? "—"} of {s.total ?? "—"} available</dd>
      </dl>
      {s.bands ? (
        <div className="bands">
          {Object.entries(s.bands).map(([band, n]) => (
            <span key={band} className="band-chip">[{band}]: {n}</span>
          ))}
        </div>
      ) : null}
    </div>
  );
}
