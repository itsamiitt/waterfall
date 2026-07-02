# 37 — Implementation Slice 15: real-provider HTTP smoke + pinned fixtures (Go)

**Status:** `IMPLEMENTED` (mainline green) · **Owner:** Staff Integrations Engineer · **Last updated:** 2026-07-01
**Builds on:** [`36`](36-Implementation-Slice-14.md) · **Canonical spec:** [`03`](03-Providers.md), [`skills/api-integration`](../skills/api-integration/SKILL.md), [`26`](26-Implementation-Slice-04.md) · **Approved by:** human (2026-07-01)

> Exercises the real vendor adapters (Hunter / Prospeo / Twilio) end-to-end through the egress
> key-injection seam against mock vendor servers, and **pins the assumed response shapes as
> checked-in fixtures** — narrowing the no-fabrication gap on vendor wire formats to a single,
> tested, clearly-labelled place.

## 1. What's verified vs still `UNVERIFIED` (honest)
Per the no-fabrication rule, only what can be checked here is claimed:
- **VERIFIED (load-bearing):** the **auth scheme + injection** (Hunter `api_key` query, Prospeo
  `X-KEY` header, Twilio HTTP Basic) and the **HTTP status → error-class mapping**. These are
  exercised against real HTTP servers through the production `AuthInjector` and `classifyStatus`.
- **`UNVERIFIED` (representative):** the JSON **field names** in the fixtures. No live vendor was
  called (no authorized key), so the shapes remain assumed — now recorded as fixtures with a
  documented promotion path (`testdata/README_UNVERIFIED.md`).

## 2. Pinned fixtures — `internal/provider/adapters/testdata`
Representative success bodies (`hunter_found`, `hunter_empty`, `prospeo_found`, `twilio_found`)
plus a `README_UNVERIFIED.md` that states the marker and the exact steps to promote a fixture to
`VERIFIED` (obtain a sandbox key → capture the raw 2xx body → reconcile `Decode` tags → record
`source_url`/`verified_date`). `live_smoke_test.go` serves each fixture from a mock and decodes
it through the real adapter, so **a fixture that drifts from `Decode` fails the build** — the
assumed contract lives in one visible place.

## 3. New coverage (`live_smoke_test.go`, mainline)
| Test | Proves |
|------|--------|
| `TestAdapters_DecodeRecordedFixtures` | each adapter decodes its pinned fixture through the injector; **empty Hunter data → no observation, not an error** |
| `TestAdapter_EgressSSRFBlocked` | a real adapter driven through `NewEgressClient` to an http/loopback host is **refused before connecting** (`ErrSSRFBlocked` → non-retryable `BAD_REQUEST`) — the SSRF choke is live on the adapter path |
| `TestAdapters_StatusErrorMatrix` | end-to-end status→class mapping: 401→AUTH, 402→QUOTA, 403→RATE_LIMIT, **404→NOT_FOUND**, 429→RATE_LIMIT, 400→BAD_REQUEST, 500→TRANSIENT, 503→PROVIDER_DOWN |

These complement the existing `adapters_test.go` (injection + success decode + Hunter 403 /
Prospeo 402 + full engine integration). Mainline suite `go build/vet/test/gofmt` clean.

## 4. Honestly out of this slice
- **No live vendor call.** Calling a real vendor requires an authorized key and explicit
  approval; without it, the field-name shapes stay `UNVERIFIED` by design. The fixtures + README
  are the mechanism to close this the moment a key is provided.
- **SSRF positive path not re-proved here.** The guard blocks loopback, so a *successful* egress
  to an allow-listed HTTPS host can't use a local mock; that allow decision is unit-tested in
  `ssrf_test.go`. This slice proves the *block* on the adapter path.
- **Only the three Slice-04 adapters.** Additional vendors (docs/03) are not added.
- **No response-time/rate-limit-header parsing** (e.g. `Retry-After`, provider-specific quota
  headers) — the breaker/retry policy (Slice 01) handles pacing generically.

## 5. Reviewer result
| Check | Result |
|-------|--------|
| Auth scheme + injection verified end-to-end (all three schemes) | PASS |
| Status→error-class matrix verified (incl. 404→NOT_FOUND) | PASS |
| SSRF choke active on the adapter egress path | PASS |
| Assumed shapes pinned as fixtures; drift fails the build | PASS |
| `UNVERIFIED` marker + promotion path documented (no fabrication) | PASS |
| Mainline `go build/vet/test/gofmt` clean | PASS |
| No live vendor call / positive-egress path honestly scoped (§4) | PASS |

**Gate:** slice `IMPLEMENTED`; the vendor wire-format risk is now localized to a single set of
labelled, tested fixtures. Proceeds to the next increment on request.
