// features/crm — lazy route boundary for /crm-connections (docs/research-intelligence/08, ADR-0030). A
// single read-only screen behind RequireRole (crm.read: operator + tenant_admin own-Tenant). The server
// re-authorizes every request — RequireRole only saves the round trip (doc 05 §1.2).
import { RequireRole } from "../../app/guards";
import CRMPage from "./CRMPage";

export function Component() {
  return (
    <RequireRole group="crm.read">
      <CRMPage />
    </RequireRole>
  );
}
