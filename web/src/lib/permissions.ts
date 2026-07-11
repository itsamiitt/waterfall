// lib/permissions.ts — client mirror of the doc 05 §2 role x action matrix, used ONLY to hide
// nav entries and soft-block routes (doc 08 §7). The server re-authorizes every request; a
// guard miss degrades to a server 403/404 rendered by the error state — never data exposure.
// GET /v1/admin/roles serves the matrix verbatim; hydrateFromServer absorbs it when fetched.

export type Role = "operator" | "tenant_admin" | "tenant_user";

export type Access = "allow" | "own-tenant-only" | "approval-gated" | "deny";

export type ActionGroup =
  | "overview.read"
  | "providers.read"
  | "providers.write"
  | "providers.stats.read"
  | "keys.read"
  | "keys.write"
  | "key_pools.manage"
  | "rotation.config"
  | "rotation.catalog.read"
  | "health.read"
  | "routing.read"
  | "routing.edit"
  | "routing.publish"
  | "workflows.read"
  | "workflows.edit"
  | "workflows.publish"
  | "queues.fleet.read"
  | "dead_letters.read"
  | "workers.read"
  | "workers.actions"
  | "cost.read"
  | "budgets.write"
  | "intent.read"
  | "research.read"
  | "ai.models.read"
  | "alerts.read"
  | "alerts.write"
  | "users.read"
  | "users.write"
  | "sessions.read"
  | "audit.read"
  | "approvals.read"
  | "approvals.decide";

type Row = Record<Role, Access>;
const row = (operator: Access, tenant_admin: Access, tenant_user: Access): Row => ({
  operator,
  tenant_admin,
  tenant_user,
});

// Doc 05 §2, reduced to the groups the SPA gates on. Deny is the default for anything absent.
const MATRIX: Record<ActionGroup, Row> = {
  "overview.read": row("allow", "own-tenant-only", "own-tenant-only"),
  "providers.read": row("allow", "allow", "allow"), // tenants see the catalog projection
  "providers.write": row("allow", "deny", "deny"),
  "providers.stats.read": row("allow", "deny", "deny"),
  "keys.read": row("allow", "own-tenant-only", "deny"),
  "keys.write": row("allow", "own-tenant-only", "deny"),
  "key_pools.manage": row("allow", "own-tenant-only", "deny"),
  "rotation.config": row("allow", "deny", "deny"),
  "rotation.catalog.read": row("allow", "allow", "allow"),
  "health.read": row("allow", "deny", "deny"),
  "routing.read": row("allow", "own-tenant-only", "own-tenant-only"),
  "routing.edit": row("allow", "own-tenant-only", "deny"),
  "routing.publish": row("approval-gated", "approval-gated", "deny"),
  "workflows.read": row("allow", "own-tenant-only", "own-tenant-only"),
  "workflows.edit": row("allow", "own-tenant-only", "deny"),
  "workflows.publish": row("approval-gated", "approval-gated", "deny"),
  "queues.fleet.read": row("allow", "deny", "deny"),
  "dead_letters.read": row("own-tenant-only", "own-tenant-only", "deny"),
  "workers.read": row("allow", "deny", "deny"),
  "workers.actions": row("allow", "deny", "deny"),
  "cost.read": row("allow", "own-tenant-only", "own-tenant-only"),
  "budgets.write": row("allow", "own-tenant-only", "deny"),
  // R&I: computed intent read (matches internal/dash/rbac IntentRead — operator allow, TA/TU own-tenant).
  "intent.read": row("allow", "own-tenant-only", "own-tenant-only"),
  // R&I: research dossier read (matches internal/dash/rbac ResearchRead — operator allow, TA/TU own-tenant).
  "research.read": row("allow", "own-tenant-only", "own-tenant-only"),
  // R&I: LLM model catalog read (matches rbac AIModelsRead — platform config, operator-only).
  "ai.models.read": row("allow", "deny", "deny"),
  "alerts.read": row("allow", "own-tenant-only", "own-tenant-only"),
  "alerts.write": row("allow", "own-tenant-only", "deny"),
  "users.read": row("allow", "own-tenant-only", "deny"),
  "users.write": row("own-tenant-only", "own-tenant-only", "deny"),
  "sessions.read": row("own-tenant-only", "own-tenant-only", "own-tenant-only"),
  "audit.read": row("allow", "own-tenant-only", "deny"),
  "approvals.read": row("own-tenant-only", "own-tenant-only", "deny"),
  "approvals.decide": row("allow", "own-tenant-only", "deny"),
};

let matrix: Record<ActionGroup, Row> = MATRIX;

/** Replace the static mirror with the matrix served by GET /v1/admin/roles (doc 04 §2.2).
 * Unknown groups in the payload are ignored; missing groups keep the static rows. */
export function hydrateFromServer(server: Partial<Record<string, Row>>): void {
  const next = { ...matrix };
  for (const [group, r] of Object.entries(server)) {
    if (group in MATRIX && r) next[group as ActionGroup] = r;
  }
  matrix = next;
}

/** Test seam / logout: restore the built-in mirror. */
export function resetMatrix(): void {
  matrix = MATRIX;
}

export function accessOf(role: Role, group: ActionGroup): Access {
  return matrix[group]?.[role] ?? "deny";
}

/** True when the role may reach the surface at all (allow, scoped, or via approval). */
export function can(role: Role, group: ActionGroup): boolean {
  return accessOf(role, group) !== "deny";
}

// ---- Route gating (doc 08 §3 guard column) ----

export interface NavModule {
  id: string;
  abbr: string;
  label: string;
  path: string;
  group: ActionGroup;
}

/** The nav-rail modules (doc 09 §0; R&I adds Intent, docs/research-intelligence/08). Hidden when
 * `can(role, group)` is false. */
export const NAV_MODULES: NavModule[] = [
  { id: "overview", abbr: "OV", label: "Overview", path: "/", group: "overview.read" },
  { id: "providers", abbr: "PR", label: "Providers", path: "/providers", group: "providers.read" },
  { id: "keys", abbr: "KY", label: "Keys", path: "/keys", group: "keys.read" },
  { id: "rotation", abbr: "RT", label: "Rotation", path: "/key-pools", group: "key_pools.manage" },
  { id: "health", abbr: "HL", label: "Health", path: "/health", group: "health.read" },
  { id: "routing", abbr: "RC", label: "Routing", path: "/routing", group: "routing.read" },
  { id: "workflows", abbr: "WF", label: "Waterfalls", path: "/workflows", group: "workflows.read" },
  { id: "queues", abbr: "QU", label: "Queues", path: "/queues", group: "dead_letters.read" },
  { id: "workers", abbr: "WK", label: "Workers", path: "/workers", group: "workers.read" },
  { id: "cost", abbr: "CO", label: "Cost", path: "/cost", group: "cost.read" },
  { id: "intent", abbr: "IN", label: "Intent", path: "/intent", group: "intent.read" },
  { id: "research", abbr: "RE", label: "Research", path: "/research", group: "research.read" },
  { id: "aimodels", abbr: "AI", label: "AI Models", path: "/ai-models", group: "ai.models.read" },
  { id: "security", abbr: "SE", label: "Security", path: "/security/sessions", group: "sessions.read" },
  { id: "alerts", abbr: "AL", label: "Alerts", path: "/alerts", group: "alerts.read" },
];

export function visibleNav(role: Role): NavModule[] {
  return NAV_MODULES.filter((m) => can(role, m.group));
}
