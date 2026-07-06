// Vite + Vitest config (doc 08 §11). No @vitejs/plugin-react: ADR-0016's dev list is closed
// (vite, typescript, vitest, playwright), so JSX runs through esbuild's automatic transform —
// identical output, no React fast-refresh in dev (full-page reload instead). Recorded in
// doc 12 OI-P8-2.
import { defineConfig } from "vitest/config";

// dashboardd default port (cmd/dashboardd config{port: 8090}).
const API_TARGET = "http://localhost:8090";

export default defineConfig({
  esbuild: { jsx: "automatic" },
  build: {
    outDir: "dist", // web/dist, served statically by cmd/dashboardd (doc 08 §11)
    target: "es2022",
    sourcemap: false,
  },
  server: {
    port: 5173,
    // Dev proxy to dashboardd, preserving cookies (same host, no origin rewrite).
    // http-proxy streams responses unbuffered, so text/event-stream flushes through.
    proxy: {
      "/v1": { target: API_TARGET },
      "/healthz": { target: API_TARGET },
      "/readyz": { target: API_TARGET },
      "/metrics": { target: API_TARGET },
    },
  },
  test: {
    environment: "node", // no jsdom/happy-dom (not in the ADR-0016 allowlist); render smokes use react-dom/server
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
