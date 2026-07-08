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
| `quickemailverification_found.json` | QuickEmailVerification | `GET /v1/verify` | UNVERIFIED |
| `myemailverifier_found.json` | MyEmailVerifier | `GET /api/validate_single.php` | UNVERIFIED |
| `mailboxvalidator_found.json` | MailboxValidator | `GET /v2/validation/single` | UNVERIFIED |
| `bouncify_found.json` | Bouncify | `GET /v1/verify` | UNVERIFIED |
| `emaillistverify_found.json` | EmailListVerify | `GET /api/verifyEmailDetailed` | UNVERIFIED |
| `trestle_found.json` | Trestle Phone Validation | `GET /3.0/phone_intel` | UNVERIFIED |
| `numlookupapi_found.json` | NumLookupAPI | `GET /v1/validate/{number}` | UNVERIFIED |
| `companyenrich_found.json` | CompanyEnrich | `GET /companies/enrich` | UNVERIFIED |
| `companies-house` (async test) | UK Companies House | `GET /search/companies` → `GET /company/{n}` | UNVERIFIED |
| `enrich-so_found.json` | Enrich.so reverse-lookup | `POST /api/v3/reverse-lookup/lookup` | UNVERIFIED |
| `voila-norbert_found.json` | Voila Norbert | `POST /search/name` | UNVERIFIED |
| `surfe` / `lemlist` (async test) | Surfe / Lemlist enrich | submit→poll | UNVERIFIED |
| `neutrinoapi_found.json` | NeutrinoAPI Phone Validate | `POST /phone-validate` | UNVERIFIED |
| `cloudmersive_found.json` | Cloudmersive Email Validate | `POST /validate/email/address/full` | UNVERIFIED |
| `abstract-email_found.json` | Abstract Email Validation | `GET /v1/?email=` | UNVERIFIED |
| `mailercheck_found.json` | MailerCheck | `POST /api/check/single` | UNVERIFIED |
| `reoon_found.json` | Reoon Email Verifier | `GET /api/v1/verify` | UNVERIFIED |
| `mails-so_found.json` | Mails.so | `GET /v1/validate` | UNVERIFIED |
| `emailhippo_found.json` | Email Hippo MORE | `GET /v3/more/json/{key}/{email}` | UNVERIFIED |
| `truelist_found.json` | Truelist | `POST /api/v1/verify_inline` | UNVERIFIED |
| `bigpicture_found.json` | BigPicture.io Company | `GET /v1/companies/find` | UNVERIFIED |
| `enformion_found.json` | Enformion Contact Enrich | `POST /Contact/Enrich` | UNVERIFIED |
| `brreg` (async test) | Norway Brønnøysund registry | `GET /enheter?navn=` → `GET /enheter/{orgnr}` | VERIFIED (live 2026-07-08 — public no-auth API) |
| `gleif_found.json` | GLEIF LEI Records | `GET /lei-records?filter[entity.legalName]=` | VERIFIED (live 2026-07-08 — public no-auth API) |
| `recherche-entreprises_found.json` | French Recherche d'entreprises | `GET /search?q=` | VERIFIED (live 2026-07-08 — public no-auth API) |
