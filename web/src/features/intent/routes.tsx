// features/intent — lazy route boundary for /intent and /intent/:domain (docs/research-intelligence/08).
// A single feature chunk serves both routes; the pathname selects list vs. per-account breakdown. The
// server re-authorizes every request — RequireRole only saves the round trip (doc 05 §1.2).
import { useLocation } from "react-router";
import { RequireRole } from "../../app/guards";
import IntentPage from "./IntentPage";
import IntentAccountPage from "./IntentAccountPage";

export function Component() {
  const { pathname } = useLocation();
  const isDetail = /^\/intent\/[^/]+/.test(pathname);
  return (
    <RequireRole group="intent.read">{isDetail ? <IntentAccountPage /> : <IntentPage />}</RequireRole>
  );
}
