# 28 — Implementation Slice 06: webhooks-out (tenant-bound) + OpenAPI (Go)

**Status:** `IMPLEMENTED` (tests green) · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`27`](27-Implementation-Slice-05.md) · **Approved by:** human (2026-07-01)

> Async result delivery to tenants, and a contract-tested OpenAPI spec for the REST surface.

## 1. Webhooks-out (`internal/webhook`)
On job completion the Dispatcher fires an `OnComplete` hook (added this slice) that delivers
a signed callback:
- **Tenant-bound (G1, docs/13 §6):** the target URL comes **only** from the delivering job's
  tenant's registered `Config`, resolved by `tenant_id` — never from request data. Tenant A's
  enriched PII can only reach tenant A's endpoint. Proven by `TestDeliver_TenantBound`
  (tenant A's result reaches A's server, **0 hits on B's**).
- **SSRF-safe:** delivered through a `clientFor(host)` factory that in production is
  `provider.NewEgressClient` with a **per-tenant allow-list** (the Slice-05 choke) — wiring
  the per-tenant list the egress slice left open.
- **Authenticated:** body is **HMAC-SHA256** signed with the tenant's secret
  (`X-Waterfall-Signature: sha256=…`, `X-Waterfall-Event`); receivers verify with
  `webhook.Verify` (constant-time).
- **Bounded + safe:** fired **after** the terminal state is durably recorded (a hook failure
  never loses a result); bounded retries with backoff; 5xx/429 retried, other 4xx terminal;
  unconfigured tenants are a no-op.

The completion hook decouples cleanly: the `job` package has no dependency on `webhook`.

## 2. OpenAPI spec + contract test
- `docs/api/openapi.json` — OpenAPI 3.0.3 for `/healthz`, `POST /v1/enrichments`,
  `GET /v1/enrichments/{id}`, `GET /v1/records/{subjectID}`, with security scheme + schemas.
- `internal/api/openapi_test.go` — a **dependency-free contract test** binding spec↔impl:
  every status code the implementation returns for a representative request (200/202/400/401/
  409/422/404/…) must be **declared** in the spec. The spec cannot silently drift from what
  the API actually returns.

## 3. Tests (8 new; 65 total)
`webhook`: sign/verify round-trip, **signed POST** (receiver verifies), **tenant-binding**
(no cross-tenant egress), skip-when-unconfigured, **bounded 5xx retries**, **4xx terminal**.
`api`: OpenAPI contract match + 409-declared.

## 4. Why no live loopback smoke
A local webhook receiver listens on `127.0.0.1`, which the Slice-05 egress guard **correctly
blocks**. So real delivery through `NewEgressClient` cannot target localhost — by design. The
delivery logic + tenant-binding are proven against a local server via an injected plain client
(`TestDeliver_*`); the egress enforcement is proven in Slice 05. Together they cover the whole
path.

## 5. Honestly out of this slice
- Dedicated **webhook-retry topic** (docs/13 §6): delivery is currently inline in the worker
  (bounded); production routes it to its own durable retry lane so a slow endpoint can't
  occupy a worker.
- Webhook endpoint **registration API** + secret rotation (registry is static/config here).
- OpenAPI **request/response body** validation (this test binds status codes; a full schema
  validator needs an external lib — deferred).
- Delivery **receipts/audit** + replay protection guidance for consumers (timestamp header).

## 6. Reviewer result
| Check | Result |
|-------|--------|
| Webhook URL only from tenant config (tenant-bound); no cross-tenant egress | PASS |
| Delivered through the per-tenant egress allow-list (SSRF-safe) | PASS |
| HMAC-signed + verifiable | PASS |
| Fires after durable-terminal; hook failure never loses result | PASS |
| Bounded retries; 4xx terminal | PASS |
| OpenAPI spec contract-tested against the implementation | PASS |
| `go build/vet/test/gofmt` clean | PASS |
| Deferred scope logged, not hidden (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; proceeds to the next increment on request.
