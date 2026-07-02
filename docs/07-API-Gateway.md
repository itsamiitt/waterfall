# 07 — API Gateway & API Architecture

**Status:** `IN-REVIEW` · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Gated by:** [api-integration](../skills/api-integration/SKILL.md) · [Security Auditor](../agents/security-auditor.md) · `/architecture-review` + `/security-audit`

> Protocol set decided in **[ADR-0012](../adr/0012-api-protocol-strategy.md)**: REST + webhooks external,
> gRPC internal, GraphQL deferred. This doc is the canonical home for external API contracts.

## 1. Gateway responsibilities (edge, per ADR-0010)
authN (OAuth2/JWT or mTLS) · authZ (RBAC/ABAC, server-side) · **tenant_id from verified principal only** →
signed request context · per-tenant rate limiting + quotas · request validation · **Idempotency-Key**
enforcement on writes/jobs (G2) · API versioning (`/v1`) · compression · response caching (safe GETs) ·
connection pooling to the modulith.

## 2. External API surface
| Endpoint class | Shape | Notes |
|----------------|-------|-------|
| Single enrich | `POST /v1/enrich` (sync) | 1 record; SLA-bounded; Idempotency-Key required |
| Batch enrich | `POST /v1/enrich/batch` (sync, small N) | capped N (DoS bound); partial results allowed |
| Bulk enrich | `POST /v1/jobs` → `202 {job_id}` | async; poll `GET /v1/jobs/{id}` or webhook |
| Results stream | `GET /v1/jobs/{id}/results` (NDJSON/chunked) | large pulls without buffering |
| Webhooks | tenant-registered callback | HMAC-signed, idempotent, delivered via egress-proxy (`13`) |
| Admin/dashboard | `GET/POST /v1/admin/*` | RBAC/ABAC (`17`); tenant-scoped |

Canonical **error model** = the `api-integration` taxonomy (`AUTH`/`RATE_LIMIT`/`TRANSIENT`/`NOT_FOUND`/
`BAD_REQUEST`/`QUOTA`/`PROVIDER_DOWN`) surfaced as stable JSON error codes + HTTP status. Contracts are an
**OpenAPI 3** spec (authored at impl); webhook event schemas documented alongside.

## 3. Performance & resilience
- **Compression** (gzip/br); **response caching** for idempotent GETs (tenant-scoped keys).
- **Connection pooling** edge→modulith; keep-alive.
- **Retry/backoff** guidance for clients (honor `Retry-After`); server sheds to async under burst.
- **Timeouts** at every hop; the sync path has a hard deadline and sheds over-budget records to async.
- **Rate limiting + batch caps** per tenant/endpoint (abuse + DoS control; `18`).

## 4. Secrets & tokens
Downstream **provider** secrets never touch the gateway — they live in the key-manager/Vault and are
injected at the egress-proxy (`13`). Tenant JWTs are short-lived + rotated; OAuth2 client-credentials for
machine tenants with single-flight token refresh.

## 5. Open items
| ID | Item | Status |
|----|------|--------|
| AG-1 | External protocol set | ✅ ADR-0012 |
| AG-2 | OpenAPI contract + webhook schemas | authored at implementation |
| AG-3 | `api-flow.mmd` | ✅ (Phase 4) |
| AG-4 | Batch/bulk size caps (DoS bound) | set with `18`/`21` |

## 6. Reviewer result (`/gate-check` Phase 7)
| Check | Result |
|-------|--------|
| Protocol decision has ADR + tradeoff | PASS (ADR-0012) |
| tenant_id from principal only (G1) | PASS |
| Idempotency-Key on writes (G2) | PASS |
| Canonical error model + versioning | PASS |
| No provider secrets at gateway (SSRF/secret hygiene) | PASS |
| Rate limiting + batch caps (DoS) | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
