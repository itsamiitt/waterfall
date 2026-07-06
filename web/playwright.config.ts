// Playwright E2E harness (doc 12 P8 acceptance #2: login -> MFA -> overview; doc 15 §T6 extends
// it to the chromium/firefox/webkit matrix).
//
// CI-GATED: the specs self-skip unless E2E_BASE_URL is set, because they need a live dashboardd
// with P0 (auth) + P7 (overview/SSE) merged, seeded via T1's provisioning API. To run:
//
//   1. start dashboardd on :8090 (its default port) with a seeded user
//   2. npx playwright install firefox webkit   (chromium is usually already present; browsers are
//                                              NOT downloaded at npm install — .npmrc
//                                              ignore-scripts=true blocks postinstall)
//   3. E2E_BASE_URL=http://localhost:8090 E2E_EMAIL=... E2E_PASSWORD=... [E2E_TOTP_SECRET=...] \
//      npm run e2e:all            (all three engines)  |  npm run e2e (default project)
//
// Screenshot tolerance: cross-engine anti-aliasing/subpixel differences must not flake visual
// assertions, so toHaveScreenshot uses a small per-pixel threshold plus a whole-image
// maxDiffPixelRatio (doc 15 §T6). `--project chromium|firefox|webkit` selects an engine.
//
// The test runner ships inside the `playwright` package ('playwright/test' subpath export), so
// @playwright/test is not needed and the ADR-0016 dev allowlist stays exact.
import { defineConfig, devices } from "playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  retries: 0,
  reporter: "list",
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:8090",
    trace: "retain-on-failure",
  },
  expect: {
    // Tolerance for cross-engine AA/subpixel noise (doc 15 §T6): a pixel counts as different only
    // beyond `threshold` (0..1 per-channel), and up to 2% of pixels may differ before a failure.
    toHaveScreenshot: {
      threshold: 0.2,
      maxDiffPixelRatio: 0.02,
    },
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "firefox", use: { ...devices["Desktop Firefox"] } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
});
