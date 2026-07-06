// features/alerts — Monitoring & Alerting (doc 09 §12). Local tabs rules | channels | events
// (these are one route, /alerts; the rule editor is the deep link /alerts/rules/:id). Live
// episodes arrive on the SSE `alert` topic inside the events feed.
import { useState } from "react";
import { Link } from "react-router";
import { isApiError } from "../../api/client";
import { Badge, Button, EmptyState } from "../../design/primitives";
import { useDeleteRule, useRules } from "./api";
import { ChannelsPanel } from "./ChannelsPanel";
import { EventsFeed } from "./EventsFeed";

type Tab = "rules" | "channels" | "events";
const TABS: Tab[] = ["rules", "channels", "events"];

function RulesTab() {
  const rules = useRules({});
  const del = useDeleteRule();
  const items = rules.data?.items ?? [];

  return (
    <section>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "var(--space-3)" }}>
        <h2>Rules</h2>
        <Link to="/alerts/rules/new">
          <Button size="sm" variant="primary">
            Create rule
          </Button>
        </Link>
      </div>

      {rules.isError ? (
        <EmptyState
          variant="error"
          title="Could not load rules"
          errorCode={isApiError(rules.error) ? rules.error.code : undefined}
          action={{ label: "Retry", onClick: () => void rules.refetch() }}
        />
      ) : rules.isPending ? (
        <div className="skeleton" style={{ height: 160 }} aria-busy="true" aria-label="Loading rules" />
      ) : items.length === 0 ? (
        <EmptyState variant="zero-data" title="No alert rules" action={{ label: "Create rule", href: "/alerts/rules/new" }} />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">name</th>
              <th scope="col">metric</th>
              <th scope="col">condition</th>
              <th scope="col">severity</th>
              <th scope="col">enabled</th>
              <th scope="col">actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((r) => (
              <tr key={r.id}>
                <td>
                  <Link to={`/alerts/rules/${r.id}`}>{r.name}</Link>
                </td>
                <td>{r.metric}</td>
                <td>
                  {r.op} {r.threshold}
                  {r.window_s ? ` / ${r.window_s}s` : ""}
                </td>
                <td>{r.severity}</td>
                <td>
                  {r.enabled ? (
                    <Badge status="ok" icon="check" label="on" />
                  ) : (
                    <Badge status="neutral" icon="slash" label="off" />
                  )}
                  {r.muted_until ? <Badge status="paused" icon="pause" label="muted" /> : null}
                </td>
                <td style={{ display: "flex", gap: "var(--space-2)" }}>
                  <Link to={`/alerts/rules/${r.id}`}>
                    <Button size="sm">Edit</Button>
                  </Link>
                  <Button size="sm" variant="danger" loading={del.isPending && del.variables === r.id} onClick={() => del.mutate(r.id)}>
                    Delete
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p style={{ color: "var(--color-text-muted)", fontSize: "var(--text-sm)" }}>
        Deleting a rule auto-resolves its open episodes (doc 04 §2.11).
      </p>
    </section>
  );
}

export default function AlertsPage() {
  const [tab, setTab] = useState<Tab>("rules");
  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Alerts</h1>
      </div>
      <nav className="p-tabs" aria-label="Alerts sections" style={{ marginBottom: "var(--space-4)" }}>
        {TABS.map((t) => (
          <button key={t} aria-current={t === tab ? "page" : undefined} onClick={() => setTab(t)}>
            {t}
          </button>
        ))}
      </nav>
      {tab === "rules" ? <RulesTab /> : tab === "channels" ? <ChannelsPanel /> : <EventsFeed />}
    </>
  );
}
