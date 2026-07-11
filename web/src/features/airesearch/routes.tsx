// features/airesearch — lazy route boundary for /research and /research/:id (docs/research-intelligence/08).
// A single feature chunk serves both routes; the pathname selects the dossier list vs. the full document
// viewer. The server re-authorizes every request — RequireRole only saves the round trip (doc 05 §1.2).
import { useLocation } from "react-router";
import { RequireRole } from "../../app/guards";
import ResearchPage from "./ResearchPage";
import DossierPage from "./DossierPage";

export function Component() {
  const { pathname } = useLocation();
  const isDetail = /^\/research\/[^/]+/.test(pathname);
  return (
    <RequireRole group="research.read">{isDetail ? <DossierPage /> : <ResearchPage />}</RequireRole>
  );
}
