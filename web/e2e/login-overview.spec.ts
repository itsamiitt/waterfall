// P8 acceptance E2E (doc 12 P8 #2): login -> (MFA) -> overview renders live tiles.
//
// CI-GATED / SKIPPED BY DEFAULT: needs a live dashboardd with P0 (auth) + P7 (overview/SSE)
// merged — the sequencing note is doc 12 OI-IP-3. How to run:
//
//   go run ./cmd/dashboardd            # listening on :8090 with a seeded user, web/dist built
//   npx playwright install chromium    # one-time; postinstall is disabled by .npmrc
//   E2E_BASE_URL=http://localhost:8090 \
//   E2E_EMAIL=ops@acme.example E2E_PASSWORD=... [E2E_TOTP_SECRET=BASE32SEED] \
//   npm --prefix web run e2e
//
import { createHmac } from "node:crypto";
import { expect, test } from "playwright/test";

const BASE = process.env.E2E_BASE_URL;
const EMAIL = process.env.E2E_EMAIL ?? "";
const PASSWORD = process.env.E2E_PASSWORD ?? "";
const TOTP_SECRET = process.env.E2E_TOTP_SECRET; // base32; omit for non-MFA test users

/** RFC 6238 TOTP (SHA-1, 6 digits, 30s step) so the spec needs no extra dependency. */
function totp(base32Secret: string, now = Date.now()): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const ch of base32Secret.replace(/=+$/, "").toUpperCase()) {
    const v = alphabet.indexOf(ch);
    if (v < 0) continue;
    bits += v.toString(2).padStart(5, "0");
  }
  const bytes = Buffer.from(bits.match(/.{8}/g)?.map((b) => parseInt(b, 2)) ?? []);
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(now / 1000 / 30)));
  const mac = createHmac("sha1", bytes).update(counter).digest();
  const offset = mac[mac.length - 1]! & 0xf;
  const code = ((mac.readUInt32BE(offset) & 0x7fffffff) % 1_000_000).toString();
  return code.padStart(6, "0");
}

test.describe("P8 acceptance: login → overview", () => {
  test.skip(
    !BASE || !EMAIL || !PASSWORD,
    "CI-gated: set E2E_BASE_URL/E2E_EMAIL/E2E_PASSWORD against a dashboardd (:8090) with P0+P7 merged (doc 12 OI-IP-3)",
  );

  test("signs in, completes MFA when demanded, and renders live overview tiles", async ({ page }) => {
    await page.goto("/login");
    await page.getByLabel("Email").fill(EMAIL);
    await page.getByLabel("Password").fill(PASSWORD);
    await page.getByRole("button", { name: "Sign in" }).click();

    // MFA-enrolled users are routed to /mfa (doc 08 §7).
    if (await page.waitForURL(/\/(mfa|$)/).then(() => page.url().includes("/mfa"))) {
      test.skip(!TOTP_SECRET, "user is MFA-enrolled: set E2E_TOTP_SECRET");
      await page.getByLabel("Authenticator code").fill(totp(TOTP_SECRET!));
      await page.getByRole("button", { name: "Verify" }).click();
    }

    // Overview shell: h1, generated_at meta, and the StatTile grid bound to GET /overview.
    await expect(page.getByRole("heading", { name: "Global overview" })).toBeVisible();
    await expect(page.locator(".page-header-meta")).toContainText("generated_at");
    await expect(page.locator(".p-stattile").first()).toBeVisible();

    // The ONE multiplexed EventSource must reach `live` (doc 04 §3.1; SSE indicator, doc 08 §5).
    await expect(page.locator(".sse-indicator")).toHaveAttribute("data-state", "live", {
      timeout: 15_000,
    });

    // Dark mode visual smoke (doc 12 P8 acceptance #5): tokens flip via data-theme.
    await page.getByRole("button", { name: "Toggle theme" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await page.screenshot({ path: "test-results/overview-dark.png", fullPage: true });
  });
});
