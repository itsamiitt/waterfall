import { afterEach, describe, expect, it } from "vitest";
import {
  accessOf,
  can,
  hydrateFromServer,
  resetMatrix,
  visibleNav,
  type ActionGroup,
} from "./permissions";

afterEach(resetMatrix);

describe("role x action mirror (doc 05 §2 — cosmetic; server is authority)", () => {
  it("operator-only surfaces are denied to tenant roles", () => {
    for (const group of [
      "providers.write",
      "health.read",
      "workers.read",
      "workers.actions",
      "queues.fleet.read",
      "rotation.config",
    ] as ActionGroup[]) {
      expect(can("operator", group)).toBe(true);
      expect(can("tenant_admin", group)).toBe(false);
      expect(can("tenant_user", group)).toBe(false);
    }
  });

  it("tenant_admin gets own-tenant-scoped access to keys/pools/budgets/users", () => {
    for (const group of [
      "keys.write",
      "key_pools.manage",
      "budgets.write",
      "users.write",
      "dead_letters.read",
    ] as ActionGroup[]) {
      expect(accessOf("tenant_admin", group)).toBe("own-tenant-only");
      expect(can("tenant_user", group)).toBe(false);
    }
  });

  it("tenant_user is read-only: overview/cost/alerts/catalog yes, writes no", () => {
    expect(can("tenant_user", "overview.read")).toBe(true);
    expect(can("tenant_user", "cost.read")).toBe(true);
    expect(can("tenant_user", "alerts.read")).toBe(true);
    expect(can("tenant_user", "providers.read")).toBe(true); // catalog projection
    expect(can("tenant_user", "alerts.write")).toBe(false);
    expect(can("tenant_user", "keys.read")).toBe(false);
  });

  it("publish/rollback is approval-gated, never plain allow", () => {
    expect(accessOf("operator", "routing.publish")).toBe("approval-gated");
    expect(accessOf("tenant_admin", "workflows.publish")).toBe("approval-gated");
    expect(can("tenant_admin", "workflows.publish")).toBe(true); // reachable, via quorum
    expect(can("tenant_user", "routing.publish")).toBe(false);
  });

  it("nav rail hides operator modules from tenant_user", () => {
    const ids = visibleNav("tenant_user").map((m) => m.id);
    expect(ids).toContain("overview");
    expect(ids).toContain("cost");
    expect(ids).toContain("security"); // own sessions
    expect(ids).not.toContain("keys");
    expect(ids).not.toContain("workers");
    expect(ids).not.toContain("health");
    expect(ids).not.toContain("queues");
  });

  it("operator sees all 12 modules", () => {
    expect(visibleNav("operator")).toHaveLength(12);
  });

  it("hydrateFromServer overrides known groups and ignores unknown ones", () => {
    hydrateFromServer({
      "alerts.write": { operator: "allow", tenant_admin: "deny", tenant_user: "deny" },
      "not.a.group": { operator: "allow", tenant_admin: "allow", tenant_user: "allow" },
    });
    expect(can("tenant_admin", "alerts.write")).toBe(false);
    resetMatrix();
    expect(can("tenant_admin", "alerts.write")).toBe(true);
  });
});
