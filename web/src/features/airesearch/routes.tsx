// features/airesearch — lazy route boundary for /research, /research/runs, and /research/:id
// (docs/research-intelligence/08). A single feature chunk serves all three; the pathname selects the
// dossier list, the async run monitor, or the full-document viewer. /research/runs is checked first
// because it also matches the /research/:id shape (a dossier id is a domain, never "runs"). The server
// re-authorizes every request — RequireRole only saves the round trip (doc 05 §1.2).
import { useLocation } from "react-router";
import { RequireRole } from "../../app/guards";
import ResearchPage from "./ResearchPage";
import DossierPage from "./DossierPage";
import RunsPage from "./RunsPage";

export function Component() {
  const { pathname } = useLocation();
  let page = <ResearchPage />;
  if (pathname === "/research/runs") {
    page = <RunsPage />;
  } else if (/^\/research\/[^/]+/.test(pathname)) {
    page = <DossierPage />;
  }
  return <RequireRole group="research.read">{page}</RequireRole>;
}
