// app/guards.tsx — RequireAuth (GET /auth/me bootstrap) and RequireRole (lib/permissions
// mirror). Guards are UX, not security: the server re-authorizes every request (doc 08 §7).
import { createContext, useContext, type ReactNode } from "react";
import { Navigate, Outlet, useLocation } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { get, isApiError, setCsrfToken } from "../api/client";
import { qk, staleTimes } from "../api/keys";
import type { AuthMe } from "../api/types";
import { can, type ActionGroup, type Role } from "../lib/permissions";
import { EmptyState } from "../design/primitives";

export interface AuthContextValue {
  user: AuthMe["user"];
  role: Role;
  tenantId: string;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function useAuth(): AuthContextValue {
  const v = useContext(AuthContext);
  if (!v) throw new Error("useAuth outside RequireAuth");
  return v;
}

async function fetchMe(): Promise<AuthMe> {
  const me = await get<AuthMe>("/auth/me");
  // Doc 08 §7 rehydration path: if the server starts returning csrf_token here, absorb it.
  if (me.csrf_token) setCsrfToken(me.csrf_token);
  return me;
}

/** Wraps every authenticated route: bootstraps the session via GET /auth/me. */
export function RequireAuth() {
  const location = useLocation();
  const me = useQuery({
    queryKey: qk.auth.me,
    queryFn: fetchMe,
    staleTime: staleTimes.config,
    retry: false,
  });

  if (me.isPending) {
    return (
      <div className="shell-main" aria-busy="true">
        <div className="skeleton" style={{ height: 240 }} />
      </div>
    );
  }
  if (me.isError) {
    if (isApiError(me.error) && me.error.status === 401) {
      const next = encodeURIComponent(location.pathname + location.search);
      return <Navigate to={`/login?next=${next}`} replace />;
    }
    return (
      <EmptyState
        variant="error"
        title="Could not load your session"
        errorCode={isApiError(me.error) ? me.error.code : undefined}
        body={me.error instanceof Error ? me.error.message : undefined}
        action={{ label: "Retry", onClick: () => void me.refetch() }}
      />
    );
  }

  const value: AuthContextValue = {
    user: me.data.user,
    role: me.data.user.role,
    tenantId: me.data.tenant_id,
  };
  return (
    <AuthContext.Provider value={value}>
      <Outlet />
    </AuthContext.Provider>
  );
}

/** Route-level role guard. Blocks with an explanatory empty state — the server would have
 * 403/404ed anyway; this only saves the round trip (doc 05 §1.2: cosmetic). */
export function RequireRole({ group, children }: { group: ActionGroup; children: ReactNode }) {
  const { role } = useAuth();
  if (!can(role, group)) {
    return (
      <EmptyState
        variant="error"
        title="You do not have access to this area"
        errorCode="forbidden"
        body="Your role does not permit this module. The server enforces this independently."
      />
    );
  }
  return <>{children}</>;
}
