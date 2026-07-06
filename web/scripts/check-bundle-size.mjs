// Initial-bundle size gate (doc 08 §10: app shell + overview < 400 KB gzipped, UNVERIFIED
// budget; harness required by doc 08 OI-UI-3). The initial set is everything index.html
// references (entry script, modulepreloaded static imports, stylesheets); lazy feature chunks
// are excluded by construction. Run after `vite build`.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const BUDGET_BYTES = 400 * 1024;

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = join(root, "dist");
const html = readFileSync(join(dist, "index.html"), "utf8");

const assets = new Set();
for (const m of html.matchAll(/(?:src|href)="\/?(assets\/[^"]+)"/g)) {
  assets.add(m[1]);
}

let total = 0;
const rows = [];
for (const asset of assets) {
  const gz = gzipSync(readFileSync(join(dist, asset))).length;
  total += gz;
  rows.push(`  ${asset}  ${(gz / 1024).toFixed(1)} KB gz`);
}

console.log("Initial bundle (index.html-referenced assets):");
for (const r of rows.sort()) console.log(r);
console.log(`  TOTAL ${(total / 1024).toFixed(1)} KB gz (budget ${(BUDGET_BYTES / 1024).toFixed(0)} KB)`);

if (total > BUDGET_BYTES) {
  console.error("Bundle size gate FAILED: initial bundle exceeds the doc 08 §10 budget");
  process.exit(1);
}
console.log("Bundle size gate OK");
