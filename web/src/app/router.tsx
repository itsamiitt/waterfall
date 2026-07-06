// app/router.tsx — the route tree (doc 08 §3). Every feature is a React.lazy chunk behind
// its routes.tsx (doc 08 §10: no feature statically imported by the shell); the shell +
// overview form the initial bundle. Tabs and detail pages are deep links added by P9–P11
// inside each feature's lazy boundary.
import { createBrowserRouter } from "react-router";
import { RequireAuth } from "./guards";
import { AppLayout } from "./shell/AppLayout";
import { EmptyState } from "../design/primitives";

const feature = (name: string) => {
  switch (name) {
    // One static switch so Vite can code-split each feature into its own chunk.
    case "providers":
      return () => import("../features/providers/routes");
    case "keys":
      return () => import("../features/keys/routes");
    case "rotation":
      return () => import("../features/rotation/routes");
    case "health":
      return () => import("../features/health/routes");
    case "routing":
      return () => import("../features/routing/routes");
    case "workflows":
      return () => import("../features/workflows/routes");
    case "queues":
      return () => import("../features/queues/routes");
    case "workers":
      return () => import("../features/workers/routes");
    case "cost":
      return () => import("../features/cost/routes");
    case "security":
      return () => import("../features/security/routes");
    case "alerts":
      return () => import("../features/alerts/routes");
    case "approvals":
      return () => import("../features/approvals/routes");
    default:
      throw new Error(`unknown feature ${name}`);
  }
};

function NotFound() {
  return (
    <EmptyState
      variant="zero-results"
      title="Page not found"
      body="The address does not match any dashboard route."
      action={{ label: "Go to overview", href: "/" }}
    />
  );
}

export const router = createBrowserRouter([
  {
    path: "/login",
    lazy: async () => ({ Component: (await import("../features/auth/LoginPage")).default }),
  },
  {
    path: "/mfa",
    lazy: async () => ({ Component: (await import("../features/auth/MfaPage")).default }),
  },
  {
    Component: RequireAuth,
    children: [
      {
        Component: AppLayout,
        children: [
          {
            index: true,
            lazy: async () => ({
              Component: (await import("../features/overview/OverviewPage")).default,
            }),
          },
          // P9 — providers / keys / rotation / health (doc 08 §3 route map)
          { path: "providers", lazy: feature("providers") },
          { path: "providers/compare", lazy: feature("providers") },
          { path: "providers/:id/*", lazy: feature("providers") },
          { path: "keys", lazy: feature("keys") },
          { path: "keys/import", lazy: feature("keys") },
          { path: "key-pools", lazy: feature("rotation") },
          { path: "key-pools/:id", lazy: feature("rotation") },
          { path: "rotation", lazy: feature("rotation") },
          { path: "health", lazy: feature("health") },
          { path: "health/:providerId", lazy: feature("health") },
          // P10 — routing / workflows / queues / workers
          { path: "routing", lazy: feature("routing") },
          { path: "routing/:scope/edit", lazy: feature("routing") },
          { path: "workflows", lazy: feature("workflows") },
          { path: "workflows/:scope", lazy: feature("workflows") },
          { path: "workflows/:scope/edit", lazy: feature("workflows") },
          { path: "queues", lazy: feature("queues") },
          { path: "queues/:name", lazy: feature("queues") },
          { path: "dead-letters", lazy: feature("queues") },
          { path: "workers", lazy: feature("workers") },
          // P11 — cost / security / alerts / approvals / settings
          { path: "cost", lazy: feature("cost") },
          { path: "budgets", lazy: feature("cost") },
          { path: "alerts", lazy: feature("alerts") },
          { path: "alerts/rules/:id", lazy: feature("alerts") },
          { path: "security/users", lazy: feature("security") },
          { path: "security/sessions", lazy: feature("security") },
          { path: "security/audit", lazy: feature("security") },
          { path: "settings", lazy: feature("security") },
          { path: "approvals", lazy: feature("approvals") },
          { path: "*", Component: NotFound },
        ],
      },
    ],
  },
]);
