// features/intent — per-account Intent Class breakdown (doc 05 explainability). Renders every computed
// class score with its confidence and contributing-signal count; the strongest class is highlighted as
// a StatTile. The full per-class list is always shown — the headline tile never replaces it.
import { useNavigate, useParams } from "react-router";
import { isApiError } from "../../api/client";
import { Button, EmptyState, StatTile, Table, type ColumnDef } from "../../design/primitives";
import { formatUtc } from "../../lib/format";
import { useIntentAccount } from "./api";
import { strongestClass } from "./logic";
import type { AccountScore } from "./types";

const columns: ColumnDef<AccountScore, unknown>[] = [
  { id: "class", header: "class", cell: (c) => c.row.original.class },
  { id: "score", header: "score", cell: (c) => c.row.original.score.toFixed(3) },
  { id: "confidence", header: "confidence", cell: (c) => c.row.original.confidence.toFixed(2) },
  { id: "signal_count", header: "signals", cell: (c) => String(c.row.original.signal_count) },
  { id: "computed_at", header: "computed", cell: (c) => formatUtc(c.row.original.computed_at) },
];

export default function IntentAccountPage() {
  const { domain = "" } = useParams();
  const nav = useNavigate();
  const q = useIntentAccount(domain);
  const scores = q.data?.scores ?? [];
  const top = strongestClass(scores);

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>{domain}</h1>
        <span className="page-header-meta">
          <Button size="sm" variant="ghost" onClick={() => nav("/intent")}>
            ← all accounts
          </Button>
        </span>
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load account intent"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 260 }} aria-busy="true" aria-label="Loading account intent" />
      ) : scores.length === 0 ? (
        <EmptyState variant="zero-data" title="No computed intent for this account" />
      ) : (
        <>
          {top ? (
            <div className="tile-grid" style={{ marginBottom: "var(--space-5)" }}>
              <StatTile
                label="Strongest class"
                value={top.class}
                sub={`${top.score.toFixed(3)} · conf ${top.confidence.toFixed(2)}`}
              />
              <StatTile label="Classes scored" value={String(scores.length)} />
            </div>
          ) : null}
          <Table
            columns={columns}
            data={scores}
            getRowId={(r) => r.class}
            caption={`Intent class scores for ${domain}`}
          />
        </>
      )}
    </>
  );
}
