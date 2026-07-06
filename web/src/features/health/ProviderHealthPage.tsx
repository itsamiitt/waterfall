// features/health — /health/:providerId page: header + the shared timeline panel (doc 09 §5.1).
import { Link, useParams } from "react-router";
import { ProviderTimelinePanel } from "./ProviderTimeline";

export default function ProviderHealthPage() {
  const { providerId = "" } = useParams();
  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Health · {providerId}</h1>
        <span className="page-header-meta">
          <Link to="/health">← all providers</Link>
        </span>
      </div>
      <ProviderTimelinePanel providerId={providerId} />
    </>
  );
}
