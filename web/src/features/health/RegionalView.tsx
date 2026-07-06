// features/health — regional health matrix (doc 09 §5.2). Screen → endpoint:
// GET /health/regional (region × provider grid).
import { EmptyState } from "../../design/primitives";
import { Badge } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { segmentStyle } from "./timelineModel";
import { useRegional } from "./api";

export function RegionalView() {
  const q = useRegional();
  if (q.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load the regional matrix"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }}
      />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 160 }} aria-busy="true" />;
  const { regions, providers } = q.data;
  if (providers.length === 0) return <EmptyState variant="zero-data" title="No regional data yet" />;

  return (
    <div className="section">
      <div className="section-title">Regional matrix</div>
      <div className="p-table-wrap">
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">Provider</th>
              {regions.map((r) => (
                <th key={r} scope="col">{r}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {providers.map((p) => (
              <tr key={p.provider_id}>
                <td>{p.display_name ?? p.provider_id}</td>
                {regions.map((r) => {
                  const state = p.cells[r];
                  if (!state) return <td key={r}>—</td>;
                  const st = segmentStyle(state);
                  return (
                    <td key={r}>
                      <Badge status={st.token} label={st.label} icon={st.noData ? "question" : st.token === "ok" ? "check" : st.token === "warn" ? "triangle" : "x"} />
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
