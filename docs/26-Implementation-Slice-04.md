# 26 — Implementation Slice 04: real provider adapters + egress key-injection seam (Go)

**Status:** `IMPLEMENTED` (tests green) · **Owner:** Principal Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`23`](23-Implementation-Slice-01.md) · **Approved by:** human (2026-07-01)

> Turns the generic `HTTPAdapter` template into concrete, API-first vendor adapters, and
> adds the **egress key-injection seam** so adapters are provably **secret-free** — the
> groundwork the SSRF egress-proxy slice will wrap.

## 1. What this slice adds
- **Egress auth-injection seam** (`internal/provider/egress.go`): a `KeyResolver`
  (pool→secret; Vault-backed in production) + an `AuthInjector` `http.RoundTripper` that
  places the credential as the request leaves — by header, query param, bearer, or HTTP
  Basic. Adapters build requests carrying only an `AuthDescriptor`; **the secret never
  enters adapter/worker memory** (ADR-0010, docs/12/13 §3).
- **Three concrete adapters** (`internal/provider/adapters/`):
  | Adapter | Vendor / call | Auth | Fills | Quirk |
  |---------|---------------|------|-------|-------|
  | `Hunter` | Email Finder (GET) | api_key **query** | work_email, email_status | **403 → RATE_LIMIT** |
  | `Prospeo` | Email Finder (POST) | `X-KEY` **header** | work_email, email_status | **402 → QUOTA** |
  | `Twilio` | Lookup v2 (GET) | **HTTP Basic** | phone_status | **404 → NOT_FOUND** |
- **Vocabulary extension:** added `first_name`, `last_name`, `full_name` (person match keys
  for email-finders) to the canonical Field list — the one place it may be extended
  (`docs/00` §7).

## 2. Honesty / no-fabrication
The auth **scheme** and **error-status mapping** for each vendor follow `docs/03` +
`skills/api-integration` and are the load-bearing contract. The exact request/response
**field names** are **REPRESENTATIVE** of each vendor's documented shape and are marked
`UNVERIFIED` in code — they must be confirmed against the vendor's live API/OpenAPI and
pinned with recorded fixtures before production. Adapters isolate that risk to
`Build`/`Decode`, so confirming a shape is a localized change.

## 3. Tests (6 new; 53 total)
Per adapter: **contract** (fixture → mapped Result), **injection-seam** (server sees the
injected key/basic-auth the adapter never held), **error-taxonomy** (403→RATE_LIMIT,
402→QUOTA). Plus **`TestAdapters_EngineIntegration`**: two real adapters driven through the
full Router+Engine+injector fill `work_email` (hunter) + `phone_status` (twilio) with G5
provenance — proving real adapters plug into the correctness-gate spine unchanged.

## 4. Honestly out of this slice
- **Live vendor validation** of wire formats + recorded fixtures per vendor (the
  `UNVERIFIED` field names).
- The **SSRF egress-proxy** (docs/13): this slice built the injection seam; the proxy adds
  the allow-list + DNS-rebinding-safe resolver + choke-point network policy around it.
- OAuth2 client-credentials token refresh at the egress tier (scheme reserved, not wired).
- Async provider patterns (e.g. Dropcontact submit→poll) and more of the `docs/03` roster.

## 5. Reviewer result
| Check | Result |
|-------|--------|
| Adapters are API-first (no scraping) | PASS |
| Secret-free: key injected at egress, not in adapter (tested) | PASS |
| Auth variety: query / header / basic all injected correctly | PASS |
| Error taxonomy per vendor quirk (403/402/404) | PASS |
| Real adapters run through Router+Engine with G5 provenance | PASS |
| Wire formats honestly marked UNVERIFIED, risk localized | PASS |
| `go build/vet/test/gofmt` clean | PASS |

**Gate:** slice `IMPLEMENTED`; the egress-proxy slice is the natural follow-on (wraps this seam).
