# Provider response fixtures — ⚠️ REPRESENTATIVE / `UNVERIFIED`

These JSON files are **representative** of each vendor's documented success response shape
(docs/03, skills/api-integration). They are **NOT captured from a live vendor call** and are
therefore marked `UNVERIFIED` per the project's no-fabrication rule. The load-bearing,
*verified* parts of each adapter are the **auth scheme** and the **HTTP status → error class**
mapping; the exact JSON **field names** here are the assumed contract and must be confirmed.

`live_smoke_test.go` decodes these fixtures through the real adapter + egress key-injection
seam, so a fixture that drifts from what `Decode` expects fails the build — they are the pinned
contract, in one place.

## How to promote a fixture from `UNVERIFIED` to `VERIFIED`

1. Obtain a sandbox/production API key for the vendor (authorized use only — never call a
   vendor without authorization).
2. Make one real request per fixture (the same shape the adapter builds) and save the raw 2xx
   response body verbatim into the corresponding file here.
3. Reconcile the adapter's `Decode` struct tags with the real field names; adjust `Decode`
   only (the request build + auth + error mapping should not need to change).
4. Record the `source_url` + `verified_date` in the adapter's honesty note and flip the marker.

| Fixture | Vendor | Endpoint (assumed) | Status |
|---------|--------|--------------------|--------|
| `hunter_found.json` / `hunter_empty.json` | Hunter.io Email Finder | `GET /v2/email-finder` | UNVERIFIED |
| `prospeo_found.json` | Prospeo Email Finder | `POST /email-finder` | UNVERIFIED |
| `twilio_found.json` | Twilio Lookup v2 | `GET /v2/PhoneNumbers/{e164}` | UNVERIFIED |
