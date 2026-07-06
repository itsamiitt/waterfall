// features/alerts/EventsFeed.tsx — EVENTS FEED (doc 09 §12.1). Episode history (firing/resolved),
// live via alert.event.fired / alert.event.resolved (SSE `alert` topic). Ack suppresses renotify;
// resolve still notifies; a re-fire clears the ack (PagerDuty semantics, doc 04 §2.11).
import { useState } from "react";
import { isApiError } from "../../api/client";
import { useSseTopics } from "../../api/sse";
import { toast } from "../../app/toast";
import { Badge, Button, EmptyState, Select, type SelectOption } from "../../design/primitives";
import { alertStateInfo } from "../../lib/status";
import { formatUtcTime } from "../../lib/format";
import { useAckEvent, useEvents, type EventFilters } from "./api";
import { SEVERITIES } from "./vocab";

const stateOpts: SelectOption[] = [
  { value: "firing", label: "firing" },
  { value: "resolved", label: "resolved" },
];
const sevOpts: SelectOption[] = SEVERITIES.map((s) => ({ value: s, label: s }));

export function EventsFeed() {
  useSseTopics(["alert"]);
  const [filters, setFilters] = useState<EventFilters>({});
  const events = useEvents(filters);
  const ack = useAckEvent();

  const items = events.data?.items ?? [];
  const active = Object.values(filters).some(Boolean);

  return (
    <section>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-end", gap: "var(--space-3)", flexWrap: "wrap", marginBottom: "var(--space-3)" }}>
        <h2>Events feed</h2>
        <div style={{ display: "flex", gap: "var(--space-2)" }}>
          <Select label="State" options={stateOpts} value={filters.state ?? ""} placeholder="any" onChange={(v) => setFilters((f) => ({ ...f, state: v }))} />
          <Select label="Severity" options={sevOpts} value={filters.severity ?? ""} placeholder="any" onChange={(v) => setFilters((f) => ({ ...f, severity: v }))} />
          {active ? (
            <Button size="sm" onClick={() => setFilters({})}>
              Clear
            </Button>
          ) : null}
        </div>
      </div>

      {events.isError ? (
        <EmptyState
          variant="error"
          title="Could not load events"
          errorCode={isApiError(events.error) ? events.error.code : undefined}
          action={{ label: "Retry", onClick: () => void events.refetch() }}
        />
      ) : events.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" aria-label="Loading events" />
      ) : items.length === 0 ? (
        <EmptyState variant={active ? "zero-results" : "zero-data"} title={active ? "No episodes match the filters" : "No episodes recorded"} />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">state</th>
              <th scope="col">rule</th>
              <th scope="col">value</th>
              <th scope="col">fired_at</th>
              <th scope="col">resolved_at</th>
              <th scope="col">ack</th>
            </tr>
          </thead>
          <tbody>
            {items.map((e) => {
              const st = alertStateInfo(e.state);
              return (
                <tr key={e.id}>
                  <td>
                    <Badge status={st.token} icon={st.icon} label={st.label} />
                  </td>
                  <td>{e.rule_name ?? e.rule_id}</td>
                  <td>{e.value}</td>
                  <td>{formatUtcTime(e.fired_at)}</td>
                  <td>{e.resolved_at ? formatUtcTime(e.resolved_at) : "—"}</td>
                  <td>
                    {e.acked_by ? (
                      `ack ${e.acked_by}`
                    ) : e.state === "firing" ? (
                      <Button
                        size="sm"
                        loading={ack.isPending && ack.variables === e.id}
                        onClick={() =>
                          ack.mutate(e.id, { onError: () => toast.error("Ack failed") })
                        }
                      >
                        Ack
                      </Button>
                    ) : (
                      "—"
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}
