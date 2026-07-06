// Playwright E2E harness (doc 12 P8 acceptance #2: login -> MFA -> overview).
//
// CI-GATED: the spec self-skips unless E2E_BASE_URL is set, because it needs a live dashboardd
// with P0 (auth) + P7 (overview/SSE) merged. To run:
//
//   1. start dashboardd on :8090 (its default port) with a seeded user
//   2. npx playwright install chromium        (browsers are NOT downloaded at npm install;
//                                              .npmrc ignore-scripts=true blocks postinstall)
//   3. E2E_BASE_URL=http://localhost:8090 E2E_EMAIL=... E2E_PASSWORD=... [E2E_TOTP_SECRET=...] \
//      npm run e2e
//
// The test runner ships inside the `playwright` package ('playwright/test' subpath export), so
// @playwright/test is not needed and the ADR-0016 dev allowlist stays exact.
import { defineConfig } from "playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  retries: 0,
  reporter: "list",
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:8090",
    trace: "retain-on-failure",
  },
});
