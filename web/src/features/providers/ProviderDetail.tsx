// features/providers — detail with 5 route-segment tabs (doc 09 §2.1). Screen → endpoint:
// GET /providers/{id} (header + config), GET /meta/enums (config form), and each tab's own
// endpoint (keys → §3 grid; health → §5 timeline; stats → /stats; history → /change-history).
// The dual badge sits in the header; actions live in ProviderActions. Live via `provider` topic.
import { useParams } from "react-router";
import { useSseTopics } from "../../api/sse";
import { EmptyState, Tabs, type TabSpec } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { formatCount, formatLatencyMs, formatPercent } from "../../lib/format";
import { KeysPanel } from "../keys/KeysPanel";
import { ProviderTimelinePanel } from "../health/ProviderTimeline";
import { ProviderBadges } from "./badges";
import { ProviderActions } from "./ProviderActions";
import { ProviderConfigForm } from "./ProviderConfigForm";
import { StatsTab } from "./StatsTab";
import { HistoryTab } from "./HistoryTab";
import { useMetaEnums, useProvider } from "./api";

const TABS: TabSpec[] = [
  { id: "config", label: "Config" },
  { id: "keys", label: "Keys" },
  { id: "health", label: "Health" },
  { id: "stats", label: "Stats" },
  { id: "history", label: "History" },
];
const TAB_IDS = new Set(TABS.map((t) => t.id));

function ConfigTab({ id }: { id: string }) {
  const provider = useProvider(id);
  const enums = useMetaEnums();
  if (provider.isPending) return <div className="skeleton" style={{ height: 320 }} aria-busy="true" />;
  if (provider.isError || !provider.data) return null;
  return <ProviderConfigForm provider={provider.data} enums={enums.data ?? {}} />;
}

export default function ProviderDetail() {
  useSseTopics(["provider"]);
  const params = useParams();
  const id = params.id ?? "";
  const splat = params["*"] ?? "";
  const seg = splat.split("/")[0] ?? "";
  const tab = TAB_IDS.has(seg) ? seg : "config";
  const q = useProvider(id);

  if (q.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load provider"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }}
      />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 120 }} aria-busy="true" aria-label="Loading provider" />;
  const p = q.data;

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Providers · {p.display_name}</h1>
      </div>

      <div className="detail-header">
        <ProviderBadges
          status={p.status}
          opState={p.op_state}
          effectiveAvailable={p.effective_available}
          unavailableReason={p.unavailable_reason}
        />
        <div className="detail-meta">
          {p.category ? <span>{p.category}</span> : null}
          <span>health {formatPercent(p.health_score)}</span>
          <span>latency {formatLatencyMs(p.avg_latency_ms)}</span>
          <span>credits {formatCount(p.credits_remaining)}</span>
          {p.priority !== undefined ? <span>priority {p.priority}</span> : null}
        </div>
      </div>

      {p.compliance_review_status === "pending" ? (
        <div className="banner" role="status">
          <strong>Pending compliance review.</strong> New providers land <code>DEPRIORITIZED</code> until reviewed (ADR-0009).
        </div>
      ) : null}

      <ProviderActions provider={p} />

      <Tabs tabs={TABS} basePath={`/providers/${encodeURIComponent(id)}`} value={tab} />

      <div style={{ marginTop: "var(--space-5)" }}>
        {tab === "config" ? <ConfigTab id={id} /> : null}
        {tab === "keys" ? <KeysPanel key={id} providerId={id} /> : null}
        {tab === "health" ? <ProviderTimelinePanel providerId={id} /> : null}
        {tab === "stats" ? <StatsTab id={id} /> : null}
        {tab === "history" ? <HistoryTab id={id} /> : null}
      </div>
    </>
  );
}
