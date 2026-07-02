# 31 — Implementation Slice 09: real JWT auth (verified signed tokens) (Go)

**Status:** `IMPLEMENTED` (tests green + live auth-matrix smoke) · **Owner:** Staff Security Engineer · **Last updated:** 2026-07-01
**Builds on:** [`30`](30-Implementation-Slice-08.md) · **Canonical spec:** [`18`](18-Security.md) §1 (tenant isolation / G1), [`12`](12-Secrets-and-Egress.md) · **Approved by:** human (2026-07-01)

> Replaces the static dev-token stand-in with **real JWT verification** (RFC 7519 / JWS), so
> the tenant principal that drives G1 is a *cryptographically verified* claim, not a lookup.
> Stdlib-only (HMAC + RSA), with the attack-surface hardening that a JWT verifier lives or
> dies by.

## 1. Verifier — `internal/auth` (RFC 7519 / RFC 7515)
`Verifier.Verify(token) → Claims`. Supports **HS256** and **RS256** with key rotation by
`kid`. The security properties are the point:

| Threat | Defense (tested) |
|--------|------------------|
| `alg: "none"` downgrade | rejected (`ErrUnsupportedAlg`) — empty/none alg never verifies |
| **Alg confusion** (RS256→HS256, RSA public key used as HMAC secret) | **alg is pinned by the KEY (`kid`), not the token header**; header alg must equal the key's alg |
| Forged / tampered content | HMAC via constant-time `hmac.Equal`; RSA via `VerifyPKCS1v15`; content-swap under a stale signature caught |
| Expired / premature | `exp` **required**; `nbf` honored; small clock **leeway** (60s default) absorbs skew |
| Wrong issuer / audience | `iss` must match; `aud` matches string **or array** form |
| Unknown / rotated key | selected by `kid`; multiple keys trusted simultaneously (rotation) |
| **Ambient tenant** | `tenant_id` **required & non-empty** — no token ⇒ no tenant, never a fallback (G1) |

Claims carry `tenant_id`, `sub`, and scopes (OAuth2 `scope` space-delimited **or** `scopes`
array). Signing lives only in a test-support package `internal/auth/authtest` (the production
package verifies, never signs).

## 2. Edge integration — `internal/api`
`api.JWTAuthenticator` implements the existing `Authenticator` seam, so the whole gateway
(rate limit, idempotency, principal-binding into context) is unchanged — only the credential
check is now cryptographic. Any verification failure returns **401 without leaking which
check failed**.

**Scope-based authorization:** `Server.WriteScope` (optional) gates write routes — a verified
token **without** the scope is **403 Forbidden** (authenticated but not authorized), distinct
from 401. `tenant.Principal` gained `Scopes` + `HasScope`.

## 3. Wiring — `cmd/enrichapi`
JWT is enabled when `JWT_HS256_SECRET` is set (`JWT_ISSUER`, `JWT_AUDIENCE`, `JWT_KID`), and
writes then require `enrich:write`. Absent that env, the service logs a warning and falls back
to static dev tokens (now carrying the write scope) so the demo still runs offline. Secrets
still never touch provider adapters — this is the *caller's* credential, verified at the edge.

## 4. Tests (6 new; 88 total)
`auth`: valid HS256; valid RS256 + rotation; **a table of rejections** (expired, not-yet-valid,
wrong iss, wrong aud, missing tenant, unknown kid, tampered payload, `alg:none`, malformed,
wrong secret, **alg-confusion**); array-audience; leeway. `api`: end-to-end JWT — valid+scope
accepted, missing-scope 403, expired 401, garbage 401, no-token 401.
**Live smoke:** JWT-enabled service, HS256 tokens minted externally (PowerShell/.NET HMAC) →
`202 / 403 / 401 / 401 / 401` across the matrix.

## 5. Honestly out of this slice
- **No JWKS fetch / kid-to-key discovery over the network** — keys are configured statically;
  a rotating JWKS endpoint (with caching + `kid` miss refresh) is future work.
- **No RS256 key loading from PEM in `cmd`** — the RS256 path is fully implemented and tested,
  but the command only wires the HS256 env for the demo. PEM/JWKS loading is a config concern.
- **No token revocation / introspection** (short `exp` + leeway is the mitigation here); no
  refresh-token flow (the gateway only *verifies* access tokens).
- **mTLS peer auth** (the other credential named in docs/18 §1) is not implemented; JWT is the
  path delivered here.
- **RBAC beyond a single write scope** — richer role/permission mapping is deferred; the
  `Scopes` plumbing is in place for it.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| `alg:none` + alg-confusion rejected (alg pinned by key) | PASS |
| exp/nbf/iss/aud validated; leeway bounded; HMAC constant-time | PASS |
| `tenant_id` required — G1 can't fall back to ambient tenant | PASS |
| kid rotation; HS256 + RS256 both verified | PASS |
| Scope authz: verified-but-unauthorized ⇒ 403, not 401 | PASS |
| 401 leaks no detail of which check failed | PASS |
| `go build/vet/test/gofmt` clean; 88 tests (6 new); live matrix | PASS |
| JWKS / mTLS / revocation / PEM-loading honestly deferred (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; proceeds to the next increment on request.
