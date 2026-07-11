// features/aimodels — lazy route boundary for /ai-models (docs/research-intelligence/08). A single
// read-only screen; the LLM registry is platform config, so the route is operator-only (RequireRole).
// The server re-authorizes every request — RequireRole only saves the round trip (doc 05 §1.2).
import { RequireRole } from "../../app/guards";
import AIModelsPage from "./AIModelsPage";

export function Component() {
  return (
    <RequireRole group="ai.models.read">
      <AIModelsPage />
    </RequireRole>
  );
}
