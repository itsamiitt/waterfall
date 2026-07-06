// ADR-0016 allowlist gate (doc 08 §1.3): fails the build when package.json dependencies
// deviate from the closed list, when a version range is not an exact pin, or when the
// lockfile is missing. Run first in `npm run build` and `npm test`.
import { readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));

// Runtime allowlist (ADR-0016 §Decision). "dnd-kit" is realized as its two published scoped
// packages (@dnd-kit/core, @dnd-kit/sortable) — recorded in doc 12 OI-P8-2.
const RUNTIME = new Set([
  "react",
  "react-dom",
  "react-router",
  "@tanstack/react-query",
  "@tanstack/react-table",
  "@tanstack/react-virtual",
  "recharts",
  "@dnd-kit/core",
  "@dnd-kit/sortable",
  "zustand",
  "qrcode",
]);

// Dev allowlist (ADR-0016 §Decision) plus the three types-only compiler shims required for
// `typescript` to compile JSX and the node-side tests (no runtime code; doc 12 OI-P8-2).
const DEV = new Set([
  "vite",
  "typescript",
  "vitest",
  "playwright",
  "@types/react",
  "@types/react-dom",
  "@types/node",
]);

const errors = [];

function check(deps, allow, kind) {
  for (const [name, range] of Object.entries(deps ?? {})) {
    if (!allow.has(name)) {
      errors.push(`${kind} dependency "${name}" is not in the ADR-0016 allowlist`);
    }
    if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(range)) {
      errors.push(`${kind} dependency "${name}@${range}" is not an exact version pin`);
    }
  }
}

check(pkg.dependencies, RUNTIME, "runtime");
check(pkg.devDependencies, DEV, "dev");

for (const key of ["peerDependencies", "optionalDependencies", "bundledDependencies"]) {
  if (pkg[key] && Object.keys(pkg[key]).length > 0) {
    errors.push(`package.json must not declare ${key} (ADR-0016 closed list)`);
  }
}

for (const hook of ["preinstall", "install", "postinstall", "prepare"]) {
  if (pkg.scripts?.[hook]) {
    errors.push(`package.json must not declare a "${hook}" lifecycle script`);
  }
}

if (!existsSync(join(root, "package-lock.json"))) {
  errors.push("package-lock.json is missing (must be committed; ADR-0016 verification)");
}

const npmrc = existsSync(join(root, ".npmrc")) ? readFileSync(join(root, ".npmrc"), "utf8") : "";
if (!/^\s*ignore-scripts\s*=\s*true\s*$/m.test(npmrc)) {
  errors.push(".npmrc must set ignore-scripts=true (ADR-0016: no postinstall execution)");
}

if (errors.length > 0) {
  console.error("ADR-0016 allowlist check FAILED:");
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}
console.log("ADR-0016 allowlist check OK");
