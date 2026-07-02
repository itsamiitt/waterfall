---
name: api-integration
description: >-
  The standard pattern for wrapping ANY external Provider API: authentication, retry with
  exponential backoff + jitter, circuit breaker, timeout, rate-limit handling, error-code mapping,
  idempotency keys, secret handling, and structured logging. Invoke when building any Provider
  adapter.
version: 1.0.0
---

# Skill: api-integration

## Purpose
One uniform adapter contract so every Provider behaves the same to the Execution Engine: same
failure taxonomy, same retry semantics, same observability. Pairs with `waterfall-correctness` (G2/G3).

## The adapter contract (every Provider adapter implements this)
```
interface ProviderAdapter {
  name: string
  capabilities: Field[]                       // which Fields it can return
  call(request, ctx): Promise<NormalizedResult>  // ctx carries tenant_id, idempotency_key, deadline, key_pool_selector (NOT the secret)
  healthCheck(): Promise<HealthStatus>
  estimateCost(request): Credits
}
```

> **Secret containment (ADR-0010 / `docs/13` §3):** the adapter **never receives the provider secret**.
> It emits an **auth descriptor** — `{scheme: 'api-key-header'|'bearer'|'oauth2-cc', header_name, key_pool_selector}`
> — and the **egress-proxy injects the actual key** from the pool at send time. A worker/adapter compromise
> therefore yields no keys. Token refresh (OAuth2) is likewise performed at the egress tier, single-flighted.

## Required behaviors

1. **Authentication (secret injected at the egress-proxy, not the adapter)**
   - The adapter selects a **key pool** and emits an **auth descriptor**; the **egress-proxy** fetches the
     secret from the secrets manager/Vault and injects it — secrets are never in adapter/worker memory,
     never hardcoded, never logged.
   - Support API-key header, bearer/OAuth2 client-credentials; **token refresh + caching happen at the
     egress tier**, single-flighted (one refresh in flight; others await it).

2. **Timeouts (bounded — G3)**
   - Separate connect timeout and total deadline; respect the caller's `ctx.deadline` (whichever is sooner).

3. **Retry with exponential backoff + full jitter**
   - Retry only **idempotent + transient** failures (429, 503, 504, network reset).
   - `sleep = random(0, min(cap, base * 2**attempt))`; cap attempts AND total wall-time.
   - Never retry non-idempotent writes without an idempotency key (G2).

4. **Rate-limit handling**
   - Honor `Retry-After` when present; otherwise back off.
   - Track per-key + per-provider token-bucket locally to avoid hitting the limit (proactive throttle).
   - On sustained 429s, shed load / rotate key in pool / trip breaker.

5. **Circuit breaker**
   - States closed→open→half-open; open on error-rate or consecutive-failure threshold.
   - Half-open probes a single request; close on success, re-open on failure.
   - Per-(provider,key) breaker so one bad key doesn't kill a healthy provider.

6. **Error-code mapping → canonical taxonomy**
   | Canonical | Examples | Router action |
   |-----------|----------|---------------|
   | `AUTH` | 401/403, invalid key | disable key, alert, failover key |
   | `RATE_LIMIT` | 429 | backoff/throttle/rotate key |
   | `TRANSIENT` | 5xx, timeouts, resets | retry then fall back |
   | `NOT_FOUND` | no match | fall through waterfall (not an error) |
   | `BAD_REQUEST` | 400, validation | do not retry; log; surface |
   | `QUOTA` | credits exhausted (often **HTTP 402**) | disable key, failover, alert (do not retry) |
   | `PROVIDER_DOWN` | breaker open | skip provider, fall back |

   **Provider-specific mappings (from `docs/01-Market-Research.md` §5 K4 — real, cited):**
   - **HTTP 402 → `QUOTA`** (credit exhaustion → failover, never retry). Distinct from 429 rate-limit.
   - **Hunter.io signals throttling with HTTP 403, not 429** → map 403→`RATE_LIMIT` for that adapter,
     not `AUTH`. Per-adapter overrides are expected; never assume a global code→meaning map.
   - **Ingest quota/credit headers** when present (`x-ratelimit-remaining.*`, `x-call-credits-spent`,
     `X-RateLimit-*`, `Retry-After`) to throttle proactively and gate budget (G4) before dispatch.

7. **Idempotency (G2)** — pass `ctx.idempotency_key` to the provider when supported; always record
   it in the local request ledger to dedupe replays.

8. **Structured logging + metrics**
   - Log: provider, key id (hashed), tenant_id, idempotency key, canonical error, latency, cost,
     attempt, breaker state. **Never** log secrets or full PII payloads.
   - Emit metrics: latency histogram, success/error counters by canonical code, cost, breaker trips.

## Checklist for a new Provider adapter
- [ ] Auth via Key Pool + secrets manager; no secret in code/logs.
- [ ] Connect + total timeout; respects caller deadline.
- [ ] Retry only transient/idempotent; jittered capped backoff.
- [ ] Proactive + reactive rate-limit handling (`Retry-After`, token bucket).
- [ ] Per-(provider,key) circuit breaker.
- [ ] Errors mapped to the canonical taxonomy with router actions.
- [ ] Idempotency key passed + ledgered.
- [ ] Structured logs + metrics; no secrets/PII leakage.
- [ ] Normalizes output to canonical Field names (`00-Project-Overview.md` §7).
