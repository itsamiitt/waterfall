// features/security — lazy route boundary for /security/{users,sessions,audit} and /settings
// (doc 12 §P11). One feature chunk serves all four; the pathname selects the page.
import { useLocation } from "react-router";
import { RequireRole } from "../../app/guards";
import SecurityPage from "./SecurityPage";
import SettingsPage from "./SettingsPage";

export function Component() {
  const { pathname } = useLocation();
  if (pathname.startsWith("/settings")) {
    return (
      <RequireRole group="sessions.read">
        <SettingsPage />
      </RequireRole>
    );
  }
  return <SecurityPage />;
}
