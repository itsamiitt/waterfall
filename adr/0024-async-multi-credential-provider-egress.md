# 24. Asynchronous & multi-credential provider egress

Date: 2026-07-07

## Status

Accepted (phased — all phases 1–4 implemented)

## Context

The 200-provider rollout (plan `closo-enrichment-architecture-200-tools`) has implemented every
provider that fits the engine's **synchronous, single-round-trip, single-header/query-credential**
model — 56 adapters across L1–L8. The remaining implementable providers do **not** fit that model,
and each names a specific gap in the egress/engine contract as it stands today:

1. **Async submit→poll.** dropcontact, icypeas, enrow, wiza, signalhire, bettercontact, infobelpro,
   verifalia (batch) accept a job, return a job id, and require polling a status endpoint until the
   result is ready. This can take far longer than the **`provider.DefaultPolicy` 3 s per-attempt
   timeout** (`internal/provider/call.go:27`). `HTTPAdapter.Fetch` is also a single `client.Do`
   (`httpadapter.go:58`) — it cannot express submit-then-poll.
2. **Match→fetch.** D&B Direct+, Explorium, Endole resolve an identifier first (cleanse/match → a
   DUNS / entity id), then fetch data blocks by that id — two sequential round-trips, call 2
   depending on call 1's body.
3. **OAuth2 client-credentials.** D&B Direct+ needs a token exchange (POST token endpoint with
   `Basic base64(key:secret)` → short-lived `access_token`, reused until expiry) before every data
   call. `AuthScheme` already declares `AuthOAuth2CC` (`provider.go:26`) but `AuthInjector.inject`
   (`egress.go:128`) has **no case for it** — a provider declaring `oauth2-cc` currently gets no
   credential injected.
4. **Path-segment / multi-header credentials.** MixRank carries its key as a **URL path segment**
   (`/v2/json/{api_key}/…`); the `AuthInjector` only writes headers/query, and a secret-free
   adapter's `Build` cannot place the key in the path. PredictLeads needs **two distinct credential
   headers**; `AuthDescriptor` models one.

The invariants that must survive any change: **G3** (every provider call is bounded, circuit-broken,
capped-retry — `provider.Call`), **secret containment** (ADR-0010: the adapter never holds a key; the
egress `AuthInjector` injects it), and the **`LeaseResolver` attribution seam** (`lease.go`: G5
key_id provenance + KM-3 trigger state machine feed).

## Decision

Extend the egress/engine contract along four independent, backward-compatible seams, phased so each
lands with tests and keeps the build green. No change weakens G3 or secret containment.

### Phase 1 — Per-adapter `CallPolicy` (implemented here)

The engine holds one global `policy` (`engine.go:36`, default 3 s) used for every `provider.Call`.
Async and match→fetch adapters legitimately need a **longer bounded budget** — but bounded is the
point: G3 is "bounded + circuit-broken + capped-retry", not "3 s". Introduce an optional interface:

```go
// provider/call.go
type PolicyOverrider interface { CallPolicy() CallPolicy }
```

The engine selects the policy per adapter: if the adapter implements `PolicyOverrider` and returns a
policy with `Timeout > 0`, that policy is used; otherwise the engine default (still overridable by
`WithPolicy`) applies. `HTTPAdapter` gains an optional `Policy *CallPolicy` field and a
`CallPolicy()` method that returns the zero policy when unset — so all 56 existing adapters (Policy
nil) are unaffected, and an async adapter declares e.g. `{Timeout: 90s, MaxAttempts: 1}` (no
resubmit-retry; it polls internally within the budget). G3 holds: the call is still hard-bounded and
breaker-guarded; only the bound changes, per adapter, explicitly.

### Phase 2 — `oauth2-cc` token exchange in `AuthInjector` (implemented here)

Add an `AuthOAuth2CC` case to `AuthInjector`: on first use for a pool, POST `AuthDescriptor.TokenURL`
with `Basic base64(secret)` (the pool secret holds `clientId:clientSecret`, mirroring Twilio's
`sid:token` basic pattern), cache the `access_token` + expiry per pool (mutex-guarded), and inject
`Authorization: Bearer <token>`; refresh when within a skew of expiry. The token-exchange POST uses
the **base** transport (not re-entrant through the injector) and its host must be on the SSRF
allow-list. `AuthDescriptor` gains `TokenURL string`. Secret containment is preserved — the adapter
still only names the pool.

### Phase 3 — `AsyncHTTPAdapter` helper (implemented here)

A sibling of `HTTPAdapter` for multi-round-trip flows, implementing `Adapter` + `PolicyOverrider`:
`Submit` (build the submit request) → `PollBuild`/`PollDecode` (loop with ctx-aware capped backoff
until a terminal state or the budget expires) → `Decode`. Match→fetch is the degenerate case
(one "poll" that is really the fetch-by-id). All within the Phase-1 budget; ctx cancellation and
deadline are honored on every sleep (never sleeps past `ctx.Done()`). Error mapping reuses
`classifyStatus`; a poll timeout is `ClassTransient`.

### Phase 4a — Path-segment credential (implemented here)

`AuthAPIKeyPath`: the adapter's `Build` writes a letters-only `PathPlaceholder` sentinel where the
key belongs; the injector replaces the first occurrence in `URL.Path` with the leased secret (and
clears `RawPath` so net/url re-encodes). Adapter still holds no secret. First consumer: MixRank
(`/v2/json/{key}/companies/match`).

### Phase 4b — Multi-header credential (implemented)

`AuthAPIKeyDualHeader` + `AuthDescriptor.SecondHeaderName`: the pool secret carries both values as
"first:second"; the injector splits and sets `HeaderName`←first, `SecondHeaderName`←second. First
consumer: PredictLeads (`X-Api-Key` + `X-Api-Token`).

### OAuth2 token-style note

The oauth2-cc injector (Phase 2) supports four token presentations via `AuthDescriptor.TokenStyle`:
"" / "basic" (Basic header + `grant_type` JSON body — D&B), "body" (form-encoded client creds —
Snov), "json" (JSON camelCase `grantType`/`clientId`/`clientSecret` — Demandbase), and "password"
(form-encoded resource-owner `grant_type=password&username&password`, pool secret "username:password"
— InfobelPRO). The token response parser accepts both `access_token`/`accessToken` and
`expires_in`/`expiresIn`.

## Consequences

- **Unblocks ~20 providers** (async + match→fetch + oauth2-cc) behind a small, tested, backward-
  compatible egress/engine surface, rather than special-casing each adapter.
- **G3 preserved and made explicit:** the per-adapter budget is still a hard timeout + breaker +
  capped retry; the ADR records that "bounded" ≠ "3 s".
- **Secret containment preserved:** oauth2-cc and path-key both inject at the egress tier; the
  adapter still only emits an `AuthDescriptor` naming a pool.
- **Backward compatible:** every existing adapter (Policy nil, non-oauth2 scheme) is unchanged; the
  `PolicyOverrider` check is a type assertion that fails closed to the engine default.
- **Cost/attribution unchanged:** G4 reserve-before-call and the `LeaseResolver` G5/KM-3 seam wrap
  the same `provider.Call`; a longer budget does not change how cost or key_id attribution work.
