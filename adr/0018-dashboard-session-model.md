# ADR 0018 — Dashboard session model: cookie sessions for the SPA, JWT for machines, mandatory TOTP MFA

- **Status:** Accepted
- **Date:** 2026-07-02
- **Deciders:** Senior Backend Engineer, Staff Security Engineer, Enterprise UX Architect
- **Phase:** Dashboard P0 · **Source:** `docs/waterfall-dashboard/05-rbac-security.md`

## Context
`dashboardd` serves two client populations under `/v1/admin/*`: human Users in the browser SPA
(ADR-0016) and machine callers (CI, scripts, future integrations). The repo already has
`internal/auth` (JWT, HS256/RS256) and the `api.Authenticator` seam producing a `tenant.Principal`.
Browser auth must survive the browser threat model: XSS exfiltration of credentials, CSRF against
cookie-authenticated writes, session fixation, and the unattended-browser case where a live session is
used to approve a destructive action. It must also support **instant server-side revocation** ("log out
that stolen laptop now") and feed the dual-GUC RLS binding (`app.current_tenant` +
`app.current_role`, ADR-0020) for G1 tenant isolation. The SSE transport (ADR-0019) uses `EventSource`,
which attaches cookies natively but cannot set custom headers — a constraint on where the credential
lives.

## Options considered
| Option | Pros | Cons | Key tradeoff surfaced |
|--------|------|------|-----------------------|
| **A. Server-side cookie sessions + CSRF double-submit for the SPA; existing JWT for machines (chosen)** | HttpOnly cookie is invisible to script — XSS cannot exfiltrate the credential; revocation is one row update, effective immediately; `sessions` table gives per-device listing and audit; works natively with `EventSource` | server-side session state to store and reap; CSRF machinery required because cookies are ambient | statefulness + CSRF discipline vs XSS-exfiltration resistance and revocability |
| B. JWT in `localStorage` for the SPA | stateless; symmetric with the machine path; no CSRF (header-borne credential) | **rejected: any XSS payload can read `localStorage` and exfiltrate the JWT**; no revocation before expiry — a stolen JWT stays valid; short-expiry-plus-refresh reinvents server-side session state anyway, with extra moving parts; `EventSource` cannot send it as a header | statelessness vs credential theft and revocation |
| C. External OAuth2/OIDC provider | enterprise SSO, offloaded MFA, standard flows | **rejected for now: an external dependency** (hosted IdP or a non-stdlib OIDC implementation) for an internal console whose user population is operators and tenant admins; adds redirect/callback surface to a deliberately one-way-egress backend | standards leverage vs dependency footprint; an `Authenticator` seam is left so an OIDC authenticator can slot in later without touching handlers |

## Decision
**Option A — split by client population, one `Principal` out the back.**

- **Browser sessions** (`sessions` table, migration 0004): id is **256-bit random**, base64url, stored
  server-side with `tenant_id`, `user_id`, `csrf_token`, `ip`, `user_agent`, `created_at`,
  `last_seen_at`, `idle_expires_at`, `absolute_expires_at`, `mfa_verified_at`, `revoked_at`. Cookie
  attributes: **HttpOnly; Secure; SameSite=Lax; Path=/**. Sessions carry **idle expiry (sliding) and
  absolute expiry**; exact durations are set in doc 05. Rows are reaped 24h post-expiry.
- **CSRF double-submit**: login returns the per-session CSRF value; every non-GET request must present
  it in the `X-CSRF-Token` header, compared against `sessions.csrf_token` server-side. GET/HEAD are
  side-effect-free by API convention, so SameSite=Lax plus double-submit covers the write surface.
- **Login**: `POST /v1/admin/auth/login` verifies PBKDF2 password hashes (SHA-256, 600k iterations,
  per-user salt); on success a new session id is issued (fixation defense: never reuse a
  pre-authentication id). **Revocation**: `DELETE /v1/admin/auth/sessions/{id}` sets `revoked_at`,
  effective on the next request; `GET /v1/admin/auth/sessions` lists live sessions for self-service.
- **MFA**: TOTP (RFC 6238, stdlib `crypto/hmac`, ±1 time-step skew, hashed recovery codes) is
  **mandatory for `operator` and `tenant_admin`** roles; the seed lives in a `secret_envelopes` row via
  `users.mfa_totp_envelope_id` (ADR-0017). **Step-up**: approval decisions demand a fresh code in
  `X-MFA-Code` per decision — `sessions.mfa_verified_at` is deliberately insufficient for approvals.
- **Machine access**: the existing `internal/auth` JWT path, presented as `Authorization: Bearer`, with
  scopes `role:<r>` plus `admin:*` action scopes. No cookies, therefore no CSRF exposure. JWTs are for
  machines only; the SPA never handles one.
- **Unification**: `internal/dash/httpx` provides a session-or-JWT `Authenticator` implementing
  `api.Authenticator` — session cookie tried first, then Bearer JWT; both yield a `tenant.Principal`
  whose role binds `app.current_role` per transaction (ADR-0020). Failures return the uniform error
  body `{"error":{"code":"...","message":"..."}}` with 401; all writes require `Idempotency-Key`.

## Rationale
The deciding force is **credential theft and revocation, and it beats statelessness**. An admin console
issues destructive, approval-gated, audit-chained actions; a credential that script can read (Option B)
converts any XSS bug into full session theft with no kill switch until expiry. HttpOnly cookies remove
that class outright, and the `sessions` table — trivial state at admin-console scale — buys instant
revocation, device visibility, and honest idle/absolute lifetimes. CSRF, the cost of ambient cookies,
is closed mechanically by SameSite=Lax plus double-submit. Option C solves problems we do not yet have
(federated workforce SSO) at the price of a dependency we have deliberately avoided; the
`Authenticator` seam keeps that door open without paying for it now.

## Consequences
- Positive: XSS cannot exfiltrate the browser credential; one-row revocation; per-decision MFA proof
  strengthens the audit claim "this human approved this"; single `Principal` shape downstream, so RBAC,
  RLS GUC binding, and audit logging are identical for humans and machines.
- Negative / accepted costs: session storage plus a reaper loop in `dashboardd`; CSRF handling in the
  SPA client (`X-CSRF-Token` on every mutation); two credential formats to document; TOTP-at-decision
  can stall urgent approvals on a lost device — recovery codes and skew tolerance are specified in doc
  05, and runbook coverage addresses approver lockout.
- Follow-ups / new ADRs triggered: an OIDC/SSO authenticator would be a new ADR plugging into the same
  seam; ADR-0019 relies on cookie-authenticated `EventSource`; ADR-0020 consumes the role binding.

## Verification
Integration tests: revoked and expired sessions reject with 401 in ≤1 request; non-GET without
`X-CSRF-Token` (or with a mismatched value) rejects 403; login rotates the session id (fixation test);
TOTP verification against RFC 6238 test vectors, including skew-window and recovery-code paths; step-up
required on approval decisions even with `mfa_verified_at` set; JWT path grants no cookie and is immune
to CSRF by construction. P0 acceptance: login + MFA + audit E2E green; session-security cases run in
the P12 security pass.
