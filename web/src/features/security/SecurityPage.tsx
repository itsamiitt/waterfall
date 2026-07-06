// features/security — Security shell (doc 09 §11): tab bar over users / sessions / audit (each is
// a route segment, deep-linkable), with the active panel role-guarded. Approvals is its own
// top-level route (/approvals); a link is offered here for convenience.
import { Link, useLocation } from "react-router";
import { RequireRole, useAuth } from "../../app/guards";
import type { ActionGroup } from "../../lib/permissions";
import { UsersPanel } from "./UsersPanel";
import { SessionsPanel } from "./SessionsPanel";
import { AuditPanel } from "./AuditPanel";

const TABS: { seg: string; label: string; group: ActionGroup }[] = [
  { seg: "users", label: "Users", group: "users.read" },
  { seg: "sessions", label: "Sessions", group: "sessions.read" },
  { seg: "audit", label: "Audit log", group: "audit.read" },
];

export default function SecurityPage() {
  const { pathname } = useLocation();
  const { role } = useAuth();
  const seg = pathname.split("/")[2] ?? "sessions";
  const active = TABS.find((t) => t.seg === seg) ?? TABS[1]!;

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Security</h1>
      </div>
      <nav className="p-tabs" aria-label="Security sections" style={{ marginBottom: "var(--space-4)" }}>
        {TABS.map((t) => (
          <Link key={t.seg} to={`/security/${t.seg}`} aria-current={t.seg === active.seg ? "page" : undefined}>
            {t.label}
          </Link>
        ))}
        <Link to="/approvals">Approvals</Link>
        <Link to="/settings">Settings</Link>
        {role === "operator" ? (
          <Link
            to="/security/provisioning"
            aria-current={seg === "provisioning" ? "page" : undefined}
          >
            Provision Tenant
          </Link>
        ) : null}
      </nav>
      <RequireRole group={active.group}>
        {active.seg === "users" ? <UsersPanel /> : active.seg === "audit" ? <AuditPanel /> : <SessionsPanel />}
      </RequireRole>
    </>
  );
}
