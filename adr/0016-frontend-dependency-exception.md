# ADR 0016 — Frontend dependency exception: React + TypeScript + Vite SPA with a pinned allowlist

- **Status:** Accepted
- **Date:** 2026-07-02
- **Deciders:** Principal Product Architect, Enterprise UX Architect, Senior Backend Engineer
- **Phase:** Dashboard P8 · **Source:** `docs/waterfall-dashboard/08-ui-architecture.md`

## Context
The Management Dashboard must ship 12 modules at the AWS-Console/Datadog/Vercel quality bar: a
virtualized grid over 1,000+ Provider Keys per Provider (design target, UNVERIFIED until load-tested),
drag-and-drop Waterfall and routing editors with live validation, time-series charting with P95/P99
overlays, MFA enrollment with QR rendering, and an SSE-driven live overview. The repo's core value is a
**zero third-party dependency Go backend** (stdlib only), and that value has held through `internal/pg`
(hand-rolled Postgres wire client), `internal/auth`, `internal/metrics`, and `internal/provider`. The
open question is whether the rule extends into the browser, where "stdlib" means hand-writing a
rendering framework, a virtualization engine, an accessibility layer, and a charting library — none of
which enforce the five gates (G1 tenant isolation through G5 provenance live entirely server-side; the
frontend is a pure consumer of `/v1/admin/*`).

## Options considered
| Option | Pros | Cons | Key tradeoff surfaced |
|--------|------|------|-----------------------|
| **A. React + TypeScript + Vite SPA in `web/`, pinned dependency allowlist (chosen)** | mature virtualization, drag-and-drop, charting, and a11y from widely-audited libraries; typed contract via OpenAPI-generated types; static `web/dist` served by `dashboardd` — no runtime beyond the browser | npm supply-chain surface; a second toolchain (node/npm) beside Go | quality-bar velocity vs purity of the zero-dependency rule |
| B. Go-served stdlib UI — `html/template` plus hand-rolled JS | zero new toolchain; single language; rule stays absolute everywhere | drag-and-drop editors, virtualized grids, and charting at the stated quality bar are **months of bespoke work** with worse accessibility than mature, widely-tested libraries; no type system in the browser; every UI bug is bespoke | absolute rule purity vs product feasibility |
| C. Next.js | React ecosystem plus batteries-included routing/SSR | adds a **Node server runtime and SSR complexity** for an authenticated internal console that gains nothing from server rendering — no SEO need, no anonymous first paint; a second long-running process to operate, patch, and secure | SSR machinery vs the fact that every page sits behind a session |

## Decision
Grant a **frontend-only exception** to the zero-dependency rule. The dashboard frontend is a
**React + TypeScript + Vite SPA** living in `web/`, built to static assets in `web/dist` and served by
`cmd/dashboardd` (API-first: the SPA is just a consumer of `/v1/admin/*`; nothing in the backend exists
only for the SPA). The dependency set is a **pinned allowlist**, closed by this ADR:

- Runtime: `react`, `react-dom`, `react-router`, `@tanstack/react-query`, `@tanstack/react-table`,
  `@tanstack/react-virtual`, `recharts`, `dnd-kit`, `zustand`, `qrcode`.
- Dev-only: `vite`, `typescript`, `vitest`, `playwright`.

No CSS framework: the design system is hand-rolled (CSS custom-property tokens + primitives).
**Adding any dependency requires amending this ADR** (append-only: a superseding ADR or a recorded
amendment reviewed at `/architecture-review`). **The zero-dependency rule remains absolute for Go**: no
Go module is added by this decision, and no future frontend need justifies one.

## Rationale
The gates the zero-dependency rule protects — auditable supply chain, no transitive surprises, code we
fully own on the trust boundary — apply with full force to the backend, which holds Provider Keys,
enforces G1 tenant isolation, and signs the audit chain. The browser bundle sits outside that boundary:
it holds no secrets (session cookie is HttpOnly per ADR-0018), performs no authorization (server-side
only), and can be rebuilt from a lockfile deterministically. Option B loses months rebuilding
commodity UI infrastructure and would still ship weaker accessibility; that cost buys no additional
gate enforcement. Option C adds a second server runtime — operational surface — for zero benefit to an
authenticated console. We chose **feasibility at the quality bar over rule purity in the browser**, and
contained the concession with a closed, pinned allowlist rather than an open door.

## Consequences
- Positive: all 12 modules buildable at the stated quality bar; typed end-to-end contract
  (OpenAPI → `types.gen.ts`); backend purity untouched; static hosting keeps `dashboardd` the only
  deployable.
- Negative / accepted costs: **npm supply-chain surface** — mitigated by a committed lockfile, exact
  version pins (no `^`/`~` ranges), `ignore-scripts=true` (no postinstall execution), CI `npm audit`
  plus lockfile-diff review on every change, and the allowlist check below. **Two toolchains** (Go +
  node) in CI and developer setup — accepted; the node toolchain touches only `web/`.
- Follow-ups / new ADRs triggered: ADR-0018 (session model the SPA authenticates with); ADR-0019
  (realtime transport the SPA consumes); any allowlist change amends this ADR.

## Verification
CI enforces: `package.json` dependencies ⊆ the allowlist above (scripted check, build fails on
violation); lockfile present and unchanged unless the diff is reviewed; `npm audit` gate; Vite build +
`vitest` + Playwright E2E green (P8 acceptance: login → overview). Bundle-size and route-level
performance budgets are set in `docs/waterfall-dashboard/08-ui-architecture.md` and remain UNVERIFIED
until measured in P11.
