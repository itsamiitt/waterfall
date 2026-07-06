// features/approvals — lazy route boundary for /approvals (doc 12 §P11).
import { RequireRole } from "../../app/guards";
import ApprovalsPage from "./ApprovalsPage";

export function Component() {
  return (
    <RequireRole group="approvals.read">
      <ApprovalsPage />
    </RequireRole>
  );
}
