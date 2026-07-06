// Global chrome (doc 09 §0): nav rail (12 modules) + top bar (search, SSE connection state,
// Tenant/role identity, theme toggle, logout) around every authenticated route.
import { useEffect, useRef } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { post, setCsrfToken } from "../../api/client";
import { useSseStatus, SseContext } from "../../api/sse";
import { useAuth } from "../guards";
import { visibleNav } from "../../lib/permissions";
import { Button, Icon } from "../../design/primitives";
import { toggleTheme } from "../theme";
import { SearchBox } from "./SearchBox";
import { useContext } from "react";

function SseIndicator() {
  const status = useSseStatus();
  const label =
    status === "live"
      ? "live"
      : status === "degraded"
        ? "degraded"
        : status === "idle"
          ? "idle"
          : "reconnecting";
  return (
    <span className="sse-indicator" data-state={status}>
      <span className="sse-indicator-dot" aria-hidden="true" />
      {/* announce on state change only (doc 08 §9) */}
      <span role="status" aria-live="polite">
        SSE: {label}
      </span>
    </span>
  );
}

export function AppLayout() {
  const { user, role, tenantId } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const sse = useContext(SseContext);
  const location = useLocation();
  const mainRef = useRef<HTMLElement>(null);

  // Route changes move focus to the new page's h1 (doc 08 §9).
  useEffect(() => {
    const h1 = mainRef.current?.querySelector("h1");
    h1?.setAttribute("tabindex", "-1");
    h1?.focus();
  }, [location.pathname]);

  async function logout() {
    try {
      await post("/auth/logout");
    } catch {
      /* session may already be gone; local teardown proceeds */
    }
    setCsrfToken(null);
    sse?.close();
    queryClient.clear();
    void navigate("/login", { replace: true });
  }

  return (
    <div className="shell">
      <nav className="shell-rail" aria-label="Modules">
        <div className="shell-rail-brand">Waterfall Admin</div>
        {visibleNav(role).map((m) => (
          <NavLink key={m.id} to={m.path} end={m.path === "/"}>
            <span className="shell-rail-abbr" aria-hidden="true">
              {m.abbr}
            </span>
            {m.label}
          </NavLink>
        ))}
      </nav>
      <header className="shell-topbar">
        <SearchBox />
        <span className="topbar-spacer" />
        <SseIndicator />
        <span className="topbar-identity">
          tenant: {tenantId} | {user.email} ({role})
        </span>
        <Button variant="ghost" size="sm" onClick={toggleTheme} aria-label="Toggle theme">
          <Icon name="refresh" />
          Theme
        </Button>
        <Button variant="ghost" size="sm" onClick={() => void logout()}>
          Log out
        </Button>
      </header>
      <main className="shell-main" ref={mainRef}>
        <Outlet />
      </main>
    </div>
  );
}
