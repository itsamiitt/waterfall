// features/health — lazy route boundary (doc 08 §3). Dispatches /health (fleet + regional) and
// /health/:providerId (timeline).
import { useParams } from "react-router";
import FleetHealth from "./FleetHealth";
import { RegionalView } from "./RegionalView";
import ProviderHealthPage from "./ProviderHealthPage";
import "./health.css";

export function Component() {
  const { providerId } = useParams();
  if (providerId) return <ProviderHealthPage />;
  return (
    <>
      <FleetHealth />
      <RegionalView />
    </>
  );
}
