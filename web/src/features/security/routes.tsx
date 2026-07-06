// features/security — lazy route boundary for /security/{users,sessions,audit,provisioning} and
// /settings (doc 12 §P11; provisioning added in doc 15 §T1). One feature chunk serves them all;
// the pathname selects the page.
import { useLocation } from "react-router";
import { RequireRole, useAuth } from "../../app/guards";
import SecurityPage from "./SecurityPage";
import SettingsPage from "./SettingsPage";
import ProvisioningPage, { ProvisioningForbidden } from "./provisioning/ProvisioningPage";

export function Component() {
  const { pathname } = useLocation();
  const { role } = useAuth();
  if (pathname.startsWith("/settings")) {
    return (
      <RequireRole group="sessions.read">
        <SettingsPage />
      </RequireRole>
    );
  }
  // Tenant provisioning is deliberately outside the doc 05 §2 role×action matrix (SEC-3, ADR-0021):
  // gate directly on the operator role, mirroring the backend's requireOperator check.
  if (pathname.startsWith("/security/provisioning")) {
    return role === "operator" ? <ProvisioningPage /> : <ProvisioningForbidden />;
  }
  return <SecurityPage />;
}
