// Placeholder page for P9–P11 feature modules: the route exists (deep-linkable, guarded)
// but the module ships in a later phase (doc 12 §P9–P11).
import { EmptyState } from "../design/primitives";
import { RequireRole } from "./guards";
import type { ActionGroup } from "../lib/permissions";

export function ComingSoon({
  module,
  phase,
  group,
}: {
  module: string;
  phase: "P9" | "P10" | "P11";
  group: ActionGroup;
}) {
  return (
    <RequireRole group={group}>
      <div className="page-header">
        <h1>{module}</h1>
      </div>
      <EmptyState
        variant="zero-data"
        title={`${module} is coming in ${phase}`}
        body="This module's backend endpoints are live; its screens land in a later frontend phase."
      />
    </RequireRole>
  );
}
