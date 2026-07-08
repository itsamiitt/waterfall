# 03 — Provider Research & Comparison Matrix

**Status:** `IN-REVIEW` · **Owner:** Research Agent + GTM Data Platform Architect · **Last updated:** 2026-07-01
**Gated by:** [provider-research](../skills/provider-research/SKILL.md) · [Research Agent](../agents/research-agent.md) · `/provider-audit` → `/architecture-review` GATE

> **Provenance.** Phase-3 workflow `wf_f5d38fad-6f3`: **56 subagents** (28 Research + 28 adversarial
> Verify), ~**1,837,484** tokens, **798** web fetches. **28 providers**, **672** claims; **223**
> VERIFIED claims re-fetched, **38 downgraded**. Combined with the **18 providers** already cited in
> [`01-Market-Research.md`](01-Market-Research.md) (Apollo, ZoomInfo, Cognism, Lusha, RocketReach,
> PDL, FullContact, Hunter, Dropcontact, Bombora, 6sense, Demandbase, Seamless, Lead411, UpLead, Clay,
> BetterContact, Clearbit), the roster is **46 providers** across all required categories.
> `verified_date` = 2026-07-01.

## 1. Legend & honesty notes
Markers: `✓✓` re-checked & confirmed · `✓` cited (not in the 8-claim re-check sample) · `⚠↓`
**downgraded to UNVERIFIED** on re-fetch · `○` UNVERIFIED. Status per **[ADR-0009](../adr/0009-provider-inclusion-exclusion-criteria.md)**.

- **Latency (p50/p95) is almost never vendor-published** → nearly all latency cells are `UNVERIFIED`
  by design; real numbers come from our own load tests (`21`), not provider claims.
- **Identity/domain-intel providers verified weakest:** Melissa (6/8 downgraded), Ekata (4/8), Diffbot
  (3/8), WhoisXML (3/8), The Companies API (2/8), G2 (2/8) — treat their specifics as provisional.
- **Accuracy/coverage figures are vendor claims**, not measured; they seed cold-start reservation
  values but are replaced by our measured hit-rates (`08`/`20`).

## 2. Executive synthesis — coverage picture & what it means

**Coverage is deep for email, thinner and pricier for verified mobile, and specialist for
signals/identity.** The 46-provider roster gives us multiple independent sources for every required
Field, which is the precondition for a waterfall to add lift:

- **Work email** is a commodity: ≥12 pay-per-match finders (Prospeo, Findymail, Snov, Enrow,
  Anymailfinder, Hunter, Apollo, UpLead, Lead411, RocketReach, Clearout, Datagma-D) all bill only on a
  hit → run several cheap ones first, gate with a verifier. This is where the waterfall is most economical.
- **Mobile / direct dial** is scarcer and more expensive: the strongest verified-mobile sources are
  Cognism (EU/UK "Diamond"), ZoomInfo, Apollo, Lusha — and the cheapest LinkedIn-keyed mobile sources
  (Kaspr, ContactOut) are **DEPRIORITIZED** on provenance (ADR-0009). Expect higher cost-per-mobile and
  place mobile late with strict cost ceilings (G4).
- **Verification is a distinct, cheap terminal lane:** email → ZeroBounce/NeverBounce/Kickbox/Emailable/
  Clearout (+ Melissa/Ekata as risk); phone → Twilio Lookup/Telnyx (+ Melissa/Ekata). These *gate*
  values (adjust confidence, ADR-0005) rather than source them.
- **Company/firmographics** has strong specialists (Diffbot, Crunchbase for **funding**, The Companies
  API, PDL, ZoomInfo) — company enrichment is a separate lane keyed on `company_domain`, run in parallel
  to contact lanes.
- **Technographics & intent are non-substitutable specialist inputs** (BuiltWith, HG Insights for tech;
  Bombora/6sense/Demandbase/G2/PredictLeads for intent) — own lanes, own cadence (batch/async), keyed on
  `company_domain`, never a fallback for contact fields.
- **Signals** (hiring/job-postings/job-changes/tech-changes/company-changes) come from PredictLeads,
  Coresignal(D), PDL — a parallel signals lane feeding the Intent Engine (`14`).

**Five providers were flagged for exclusion; ADR-0009 resolves the inconsistency:** 2 stay **EXCLUDED**
on hard grounds (Proxycurl — LinkedIn litigation + wind-down; Datanyze — defunct/absorbed, no API); 3
are **DEPRIORITIZED** (Kaspr, ContactOut, Coresignal) — legitimate/near-legitimate APIs whose
public-web/LinkedIn provenance puts them in the *same risk class as Apollo/ZoomInfo* (which we already
accept), so they are compliance-gated, **pending an explicit human policy decision** (PR-EXCL-1).

## 3. Capability → provider coverage map + seed ordering (feeds ADR-0007)

This is the routing-critical artifact: per canonical Field, the **seed** waterfall order (cold-start,
per ADR-0007 reservation-value logic using pay-per-match economics + regional strength). It is a
**starting** order — replaced by measured reservation values once we have hit-rate data (OR-4/WQ-3).
`(D)` = DEPRIORITIZED (compliance-gated, off by default), `(E)` = EXCLUDED. Region tags note strengths.

| Field / Category | Seed primary (first / parallel-cheap) | Fallback tier | Verify / gate | Regional notes |
|------------------|---------------------------------------|---------------|---------------|----------------|
| **work_email** | Prospeo, Findymail, Enrow, Anymailfinder, Hunter (all pay-per-hit) | Apollo, Snov, UpLead, Lead411, RocketReach, Clearout | ZeroBounce / NeverBounce / Kickbox / Emailable | Datagma(D)/Dropcontact strong EU/FR |
| **personal_email** | (thin) FullContact, PDL | Melissa; ContactOut(D), Kaspr(D) | verify lane | scarce; consent-sensitive |
| **mobile_phone** | Cognism (EU/UK), ZoomInfo (US), Apollo, Lusha | Prospeo, Findymail, Enrow, Datagma(D); Kaspr(D)/ContactOut(D) | Twilio/Telnyx line-type | Cognism EU/UK · ZoomInfo US |
| **direct_dial** | ZoomInfo, Apollo, Cognism | Findymail, Enrow; ContactOut(D)/Kaspr(D) | Twilio/Telnyx | US strong; EU thinner |
| **office_phone** | ZoomInfo, company data | Melissa, BuiltWith | Twilio/Telnyx | — |
| **linkedin_url** | PDL, Apollo, RocketReach | Prospeo, Findymail, Snov, Diffbot, Crunchbase | — | ContactOut(D)/Proxycurl(E) excluded |
| **company_domain / firmographics** | Diffbot, PDL, The Companies API, ZoomInfo | Apollo, Crunchbase, Clearbit(deprio); Coresignal(D) | — | global |
| **employee_count / industry** | Diffbot, PDL, The Companies API | Crunchbase, ZoomInfo, Apollo | — | — |
| **technographics** | BuiltWith, HG Insights | The Companies API, PredictLeads; Datanyze(E), Datagma(D) | — | BuiltWith global |
| **intent_topics** | Bombora, 6sense, Demandbase (Phase 1) | G2 Buyer Intent, HG Insights, PredictLeads | — | account-level, batch cadence |
| **funding_stage** | Crunchbase (authoritative) | Diffbot, PDL, PredictLeads | — | — |
| **hiring / job-postings / job/tech/company-changes (signals)** | PredictLeads, PDL | Coresignal(D) | — | parallel signals lane → `14` |
| **email_status (verification)** | ZeroBounce, NeverBounce, Kickbox, Emailable, Clearout | Melissa, Ekata (risk) | — | terminal gate (ADR-0005) |
| **phone_status (verification)** | Twilio Lookup, Telnyx | Melissa, Ekata | — | Twilio global · Telnyx US/NANP |
| **identity resolution / person intelligence** | FullContact, PDL | Diffbot, Melissa, Ekata | — | stitch emails/phones→persistent id |
| **domain intelligence** | WhoisXML API | Clearout (domain) | — | parallel domain branch |

**Ordering principles applied (ADR-0007):** (1) *pay-per-match-first* — misses are free, so cheap
finders lead; (2) *verify last* — cheap verifiers gate before a value is billed/emitted (`01` K3); (3)
*region-aware* — EU records route to Cognism/Datagma/Dropcontact early, US to ZoomInfo/Apollo; (4)
*separate lanes per field-type* (email ≠ mobile ≠ company ≠ intent ≠ domain) run in parallel, each with
its own stop gate; (5) DEPRIORITIZED/EXCLUDED providers are **not** in the default production order.

## 4. Phase-3 provider comparison matrix

> Markers: `✓✓` re-checked & confirmed · `✓` cited (not in sample) · `⚠↓` **downgraded** on re-fetch · `○` UNVERIFIED. Status reflects **ADR-0009** (research-agent verdicts preserved in §5 entries).

| Provider | Category | Auth | Bulk (max) | Rate limit | Pricing | Compliance (S/I/G/C) | Status |
|---|---|---|---|---|---|---|---|
| **Prospeo** | business-email + linkedin… | ✓✓ API key in custom HTTP head… | ✓✓ Yes: POST /bulk-enric… | ✓✓ Enrich endpoints: 5 req/s… | ✓ Credit-based, pay-per-match —… | ○/○/?/○ | ACTIVE-CANDIDATE |
| **Findymail** | business-email finder + v… | ✓✓ Bearer token in Authorizati… | ✓✓ No dedicated bulk ema… | ✓✓ No global requests-per-se… | ✓ Credit-based subscription, pa… | Y/○/Y/? | ACTIVE-CANDIDATE |
| **Snov.io** | business-email finder + v… | ✓✓ OAuth 2.0 client_credential… | ✓✓ Yes — batch endpoints… | ✓✓ 60 requests per minute (~… | ✓ Credit-based, 1 credit per pr… | N/N/Y/Y | ACTIVE-CANDIDATE |
| **Datagma** | email + phone + enrichmen… | ✓✓ API key pair passed as quer… | ✓✓ No dedicated bulk/bat… | ✓✓ 10 requests per second (p… | ✓ Credit subscription (monthly … | ○/○/Y/○ | DEPRIORITIZED |
| **Enrow** | email finder + waterfall … | ✓✓ API key passed in an HTTP r… | ✓✓ Yes. Bulk endpoint PO… | ⚠↓ Up to 50 requests per sec… | ✓ Pay-per-valid-result (success… | ○/○/?/? | ACTIVE-CANDIDATE |
| **Anymailfinder** | business-email finder | ✓✓ API key passed in the 'Auth… | ✓✓ Yes. Async bulk email… | ✓✓ No published per-second/R… | ✓ Credit-based monthly/annual s… | ○/○/Y/○ | ACTIVE-CANDIDATE |
| **ZeroBounce** | email verification | ✓✓ API key passed as URL query… | ⚠↓ Yes. Synchronous batc… | ✓✓ Single validate: 80,000 r… | ⚠↓ Credit-based. 1 credit per em… | Y/Y/Y/Y | ACTIVE-CANDIDATE |
| **NeverBounce (ZoomInfo)** | email verification | ✓✓ Static API key, format secr… | ✓✓ Yes. POST /v4.2/jobs/… | ○ No per-second RPS limit p… | ✓ Pay-as-you-go credit model (1… | Y/Y/?/? | ACTIVE-CANDIDATE |
| **Kickbox** | email verification | ⚠↓ API key. Passed as `apikey`… | ✓✓ Yes — asynchronous ba… | ○ Not published in accessib… | ✓✓ Transparent pay-as-you-go cre… | Y/Y/Y/○ | ACTIVE-CANDIDATE |
| **Emailable** | email verification | ✓✓ API key or OAuth2 access to… | ✓✓ Yes. POST /v1/batch a… | ✓✓ Per-endpoint, per-second:… | ✓ Credit-based, 1 credit per ve… | Y/○/Y/Y | ACTIVE-CANDIDATE |
| **Twilio Lookup** | phone verification / line… | ✓✓ HTTP Basic auth: API key SI… | ✓✓ No dedicated bulk/bat… | ✓✓ No fixed public per-secon… | ✓ Pay-as-you-go, billed per req… | Y/Y/Y/Y | ACTIVE-CANDIDATE |
| **Telnyx Number Lookup** | phone verification / carr… | ✓✓ HTTP Bearer token — Telnyx … | ✓✓ No bulk/batch Lookup … | ○ Rate limiting is enforced… | ✓✓ Pay-as-you-go, charged per qu… | Y/Y/?/? | ACTIVE-CANDIDATE |
| **Kaspr** | mobile + direct-dial (EU) | ✓✓ API key passed in the 'Auth… | ○ No bulk/batch API end… | ✓✓ Business plan advertises … | ✓✓ Credit-based SaaS subscriptio… | ○/○/?/? | DEPRIORITIZED |
| **ContactOut** | business + personal email… | ✓✓ API key passed in HTTP head… | ✓✓ Yes — batch endpoints… | ✓✓ No per-second limit publi… | ✓ Credit-based subscription. In… | Y/○/Y/Y | DEPRIORITIZED |
| **Coresignal** | company + employee data-a… | ✓✓ API Key sent in the `apikey… | ✓✓ Yes — Bulk Collect en… | ✓✓ Per-endpoint per-second l… | ✓ Credit-based subscriptions wi… | ○/?/?/Y | DEPRIORITIZED |
| **Crunchbase** | company + funding data | ✓✓ API key (token). Passed eit… | ✓✓ No dedicated bulk / b… | ✓✓ 200 API calls per minute … | ⚠↓ Custom annual enterprise lice… | Y/○/?/? | ACTIVE-CANDIDATE |
| **Diffbot** | company / knowledge-graph… | ✓✓ Token-based. Single API tok… | ✓✓ Yes — Bulk Enhance: P… | ⚠↓ Per-plan calls/sec: Free … | ✓ Credit-based monthly subscrip… | ○/○/Y/Y | ACTIVE-CANDIDATE |
| **The Companies API** | company / firmographics | ✓✓ API token via HTTP header '… | ⚠↓ Bulk enrichment suppo… | ✓✓ Plan-tiered requests/seco… | ✓ Credit-based monthly subscrip… | ○/○/○/○ | ACTIVE-CANDIDATE |
| **BuiltWith** | technographics | ✓✓ API key. Two methods: query… | ✓✓ Yes. Domain API accep… | ✓✓ 10 requests/second (respo… | ✓✓ Subscription + API-credit mod… | Y/Y/Y/? | ACTIVE-CANDIDATE |
| **HG Insights** | technographics + IT intent | ✓✓ API key as HTTP Bearer toke… | ✓✓ Yes — synchronous bat… | ✓✓ 25 requests per second ac… | ✓✓ Credits-based annual subscrip… | Y/○/?/? | ACTIVE-CANDIDATE |
| **Datanyze** | technographics | ✓✓ Legacy/deprecated technogra… | ○ No documented bulk/ba… | ○ No per-second rate limit … | ✓✓ Credit-based self-serve subsc… | Y/Y/?/? | EXCLUDED |
| **PredictLeads** | signals: job postings, te… | ✓✓ API key authentication: X-A… | ✓✓ No synchronous bulk/b… | ○ Not publicly published. A… | ✓✓ Pay-as-you-go credit model. 1… | Y/○/?/? | ACTIVE-CANDIDATE |
| **G2 Buyer Intent** | intent data | ✓✓ Bearer access token generat… | ✓✓ No REST bulk/batch ma… | ○ Not published | ⚠↓ Annual subscription; Buyer In… | Y/N/Y/Y | ACTIVE-CANDIDATE |
| **Melissa** | identity resolution + add… | ✓✓ License key — a 'license st… | ⚠↓ Yes — Melissa web/Lis… | ○ Not published. | ⚠↓ Credit/consumption-based: pre… | Y/○/?/Y | ACTIVE-CANDIDATE |
| **Ekata (Mastercard)** | identity verification / r… | ✓✓ Mastercard Developers uses … | ○ No documented bulk/mu… | ○ Not publicly published (c… | ⚠↓ Custom / usage-based; not pub… | Y/Y/Y/Y | ACTIVE-CANDIDATE |
| **WhoisXML API** | domain intelligence | ✓✓ API key via apiKey query pa… | ✓✓ Yes — Bulk WHOIS API;… | ⚠↓ WHOIS API standard limit … | ⚠↓ Credit + subscription based; … | ○/○/?/? | ACTIVE-CANDIDATE |
| **Clearout** | email verification + find… | ✓✓ Bearer token (JWT). Header:… | ✓✓ Yes — POST /email_ver… | ✓✓ Not published as requests… | ✓ Credit-based prepaid. Plans: … | Y/?/Y/○ | ACTIVE-CANDIDATE |
| **Proxycurl / Nubela** | linkedin person/company d… | ✓✓ Static API key passed as Be… | ○ No dedicated bulk/bat… | ⚠↓ 300 requests/minute (~5 r… | ✓✓ Credit-based. Monthly subscri… | ○/○/○/○ | EXCLUDED |

## 5. Phase-3 per-provider detailed entries (cited)

> `⚠↓` rows are treated as **UNVERIFIED** downstream. Where ADR-0009 changed the status, both the research-agent verdict and the final status are shown.

### Prospeo
- **Category:** business-email + linkedin-email finder (B2B email/mobile enrichment API)
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** work_email, mobile_phone, linkedin_url, job_title, company_domain, employee_count, industry, technographics, funding_stage, email_status, phone_status
- **Summary:** Prospeo is a legitimate, API-first B2B contact-data provider (not a scraper-for-hire), offering a clean REST/JSON API over https://api.prospeo.io. Core value is work-email finding with vendor-claimed 98%+ verification accuracy plus optional mobile numbers, all on a pay-per-match credit model (no charge when no result). It accepts a LinkedIn URL as an enrichment identifier (standard, ToS-compliant enrichment), so it fits ADR-0002 (API-first, no scraping); the consumer-facing "LinkedIn email finder" is a user-driven Chrome extension separate from the API. Per its Trust Center, data is sourced f…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All eight source_urls resolve to genuine Prospeo API docs pages matching the cited attributes — no fabricated citations…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key in custom HTTP header 'X-KEY'; keys managed at app.prospeo.io/api (multiple keys supported). Content-Type: application/json, HTTPS only. | [link](https://prospeo.io/api-docs/authentication) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON over HTTPS. Host: api.prospeo.io. All endpoints are POST except Account Information (GET). Example: POST https://api.prospeo.io/enrich-person. | [link](https://prospeo.io/api-docs/authentication) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes: POST /bulk-enrich-person and /bulk-enrich-company, max 50 records/companies per call. Returns total_cost, matched[], not_matched[], invalid_datapoints[]. | [link](https://prospeo.io/api-docs/bulk-enrich-person) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | No async jobs/callbacks. Bulk endpoints are synchronous — process the batch and return matched/not_matched results in a single response. | [link](https://prospeo.io/api-docs/bulk-enrich-person) _primary_ | 2026-07-01 |
| ✓✓ | pagination | Search endpoints use page-based pagination: 'page' parameter (defaults to 1), 25 results per page, up to 1,000 pages = 25,000 max retrievable results. | [link](https://prospeo.io/api-docs/search-person) _primary_ | 2026-07-01 |
| ○ | webhooks | No webhooks documented; the API exposes only synchronous request/response endpoints (enrich, bulk-enrich, search, account). No callback/event delivery mechanism found. | — |  |
| ✓✓ | rate_limit_rps | Enrich endpoints: 5 req/s (Starter & Growth), 30 req/s (Pro). Search endpoints: 1 req/s (Starter), 2 req/s (Growth), 5 req/s (Pro). | [link](https://prospeo.io/api-docs/rate-limits) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_day | Enrich per-day: 2,000 (Starter) / 5,000 (Growth) / 500,000 (Pro). Search per-day: 1,000 / 4,000 / 250,000. Per-minute caps also apply (300 enrich, 30-180 search). | [link](https://prospeo.io/api-docs/rate-limits) _primary_ | 2026-07-01 |
| ○ | concurrency | No explicit concurrency limit documented; throughput is governed by the per-second/minute/day rate limits only. | — |  |
| ○ | latency_p50_ms | Not published by vendor. | — |  |
| ○ | latency_p95_ms | Not published by vendor. | — |  |
| ⚠↓ | coverage | 200M+ contacts and 30M+ companies (Search Person / Search Company); global B2B including US and EU; returns work email + mobile. | [link](https://prospeo.io/api-docs) _primary_ | 2026-07-01 |
| ✓ | accuracy | Vendor self-reported: 98%+ email verification accuracy; addresses marked 'valid' have <5% bounce rate. Real-time verification with status + MX provider returned. | [link](https://prospeo.io/email-finder) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based, pay-per-match — 'You won't be charged if no results are found'. Email = 1 credit, mobile = 10 credits (email included). Charged only on successful match. | [link](https://prospeo.io/api-docs/enrich-person) _primary_ | 2026-07-01 |
| ✓ | price_per_match | 1 credit per email found; 10 credits per mobile (email free). Plan tiers (vendor/3rd-party): Free 100 credits/mo; Starter $39/1,000; Growth $99/5,000; Business $369/50,000; add-ons from $10/1,000 (~$0.01/credit). | [link](https://prospeo.io/pricing) _secondary_ | 2026-07-01 |
| ✓ | error_codes | JSON responses carry boolean 'error' + 'error_code'. HTTP 200 success; 400 error (e.g. INVALID_API_KEY, NO_MATCH); 429 rate-limit exceeded. Example: {"error":true,"error_code":"NO_MATCH"}. | [link](https://prospeo.io/api-docs/authentication) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | On limit, returns HTTP 429. Reset info via response headers x-minute-reset-seconds, x-daily-reset-seconds, x-minute-request-left, x-daily-request-left. No explicit Retry-After or documented backoff/auto-retry. | [link](https://prospeo.io/api-docs/rate-limits) _primary_ | 2026-07-01 |
| ○ | sdks | No official language SDKs documented; integration is raw REST + an MCP server (api-docs/mcp) for AI tools. Third-party integrations exist (Clay, Apify, Cargo). | — |  |
| ✓ | api_versioning | Unversioned REST — endpoints have no version segment (e.g. https://api.prospeo.io/enrich-person). No documented versioning scheme; a prior /api-docs/email-finder endpoint is marked [Deprecated]. | [link](https://prospeo.io/api-docs/authentication) _primary_ | 2026-07-01 |
| ○ | regional_availability | No region-specific endpoints or data-residency options documented; single api.prospeo.io host serving global data. | — |  |
| ○ | soc2 | No SOC 2 attestation published; Trust Center cites annual third-party penetration testing but no SOC 2 report. | — |  |
| ○ | iso27001 | No ISO 27001 certification published/claimed. | — |  |
| ✓ | gdpr | GDPR-aligned: processes only professional B2B data on a lawful basis, supports data-subject rights (access/rectification/erasure/objection), signs DPAs on request, 72h breach notification (Art.33), opt-out mechanism; no DPO (does not meet Art.37 criteria). | [link](https://prospeo.io/trust-center-legal-GDPR) _primary_ | 2026-07-01 |
| ○ | ccpa | No explicit CCPA/CPRA attestation confirmed; Trust Center references 'GDPR and other global data protection frameworks' and an /optout page exists, but a specific CCPA statement was not verified. | — |  |

**Downgraded on re-check:** `coverage` (no: 200M+ contacts, 30M+ companies, Search Person/Company, and work email + mobile are suppor…)

- **Waterfall placement:** FIRST / early-tier for WORK EMAIL (work_email + email_status), globally and especially US+EU. Rationale (ADR-0007 reservation-value): pay-per-match at 1 credit (~$0.01) with no charge on miss makes its expected cost per attempt very low, and vendor-claimed 98% verify accuracy with real-time status/MX means high reservation value at the top of the email waterfall. PREFERRED FIRST when the input key is a LinkedIn URL — /enrich-person accepts linkedin_url directly, so it is a strong LinkedIn-URL-to-email step (the 'linkedin-email finder' role) ahead of name+domain-only providers. PARALLEL/FALLBACK for MOBILE_PHONE (10 credits each): usable as a secondary mobile source but not first-choice vs specialized mobile/waterfall phone providers; gate behind cheaper mobile hits. FALLBACK for firmographics/technographics/funding (company_domain, employee_count, industry, technographics, funding_stage) — usable as enrichment backfill, not a primary firmographics source. Do NOT place in a batch/async-critical path: bulk is synchronous, capped at 50/call, with no webhooks. Compliance-sensitive deployments (regulated buyers requiring SOC 2/ISO 27001 or explicit CCPA) should down-rank Prospeo until those attestations are confirmed.

### Findymail
- **Category:** business-email finder + verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** work_email, email_status, direct_dial, mobile_phone, phone_status, linkedin_url, job_title, company_domain, employee_count, industry, technographics
- **Summary:** Findymail is a legitimate API-first B2B email finder + real-time email verifier (plus US phone finder, company/technographic enrichment, and lead search). It exposes a clean REST/JSON API (Bearer auth, base https://app.findymail.com/api) with async jobs, webhooks, and pagination, backed by a machine-readable OpenAPI spec + Postman collection. Economics are pay-for-results (credits only charged on a found/verified hit) with a <5% bounce-rate guarantee and real-time SMTP verification — a strong fit for a low-reservation-value first call in the work_email waterfall. Data-sourcing is framed as a …
- **Adversarial verify:** 8 sampled → 8 confirmed, 0 downgraded. The source_url https://app.findymail.com/docs/ resolves to a genuine, publicly accessible Findymail API documentation p…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Bearer token in Authorization header: 'Authorization: Bearer {API_KEY}'; key issued from dashboard at /user/api-tokens. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON API. Base URL https://app.findymail.com/api ; Content-Type application/json. Core POST endpoints: /api/verify (verify email), /api/search/name (find email from name+company), /api/search/business-profile (email from LinkedIn URL), /api/search/company, /api/search/employees, /api/search/re… | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | No dedicated bulk email-find/verify batch endpoint documented; find/verify are single-record POST calls. Bulk-scale lead generation is via the Intellimatch async export (paginated, max 500 results/page). | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes. Intellimatch semantic search returns a hash for status/data polling; Domain search runs async jobs up to 24h; large exclusion-domain adds (>50) are queued for background processing. Completion delivered via webhook. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓✓ | pagination | Yes. page/per_page query params with current_page/total/last_page in responses; Intellimatch data endpoint caps at 500 records per page. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes. webhook_url parameter supplied on async operations to receive completion callbacks. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | No global requests-per-second limit published; throttling is concurrency-based (see concurrency). Only endpoint-specific per-minute cap documented: Technologies search = 10 requests/minute. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No daily request cap published; consumption is governed by the plan's monthly credit allowance rather than a daily API-call limit. | — |  |
| ✓✓ | concurrency | 300 simultaneous requests by default across all endpoints. Per-endpoint overrides: Domain search limited to 5 concurrent synchronous requests; Business-profile search limited to 30 concurrent synchronous requests. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ○ | latency_p50_ms | Not published. Verification runs in real-time at request; async searches (domain/Intellimatch) can take minutes to 24h and are not comparable to synchronous latency. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ✓ | coverage | No absolute coverage percentage published. Vendor self-reports ranked #1 Email Finder in Coverage by Clay and claims '23% more valid emails than competitors' on catch-all domains. | [link](https://www.findymail.com/) _primary_ | 2026-07-01 |
| ✓ | accuracy | <5% bounce-rate guarantee — credits refunded if emails delivered by Findymail exceed a 5% bounce rate. Emails verified in real-time at request time (syntax, spam traps, SMTP handshake, catch-all deliverability). Self-reported #1 in Accuracy (Clay). | [link](https://www.findymail.com/) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based subscription, pay-for-results: 'You only pay for verified results. No charge if we can't find it.' 1 credit = 1 verified email, 10 credits = 1 phone number. Unused credits roll over up to 2x the monthly allowance. Starter $99/mo = 5,000 Finder + 5,000 Verifier credits; Enterprise = cus… | [link](https://www.findymail.com/pricing/) _primary_ | 2026-07-01 |
| ✓ | price_per_match | ~$0.0208 (2.08¢) per usable email on the Starter plan; phone number = 10 credits (~$0.21-equivalent). No charge on unsuccessful lookups. | [link](https://www.findymail.com/pricing/) _primary_ | 2026-07-01 |
| ✓ | error_codes | 200 OK, 201 Created, 204 Deleted, 402 Insufficient credits, 404 Not found, 422 Validation failed, 423 Subscription paused, 429 Rate limit exceeded. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | No explicit retry/backoff or Retry-After policy documented. 429 signals a concurrency/rate breach (caller should back off); long-running work uses async jobs + webhook/polling instead of synchronous client retries. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓ | sdks | No official language SDKs. Machine-readable OpenAPI spec (openapi.yaml) and Postman collection (collection.json) provided. Third-party connectors exist: Clay, Make, Pipedream, Nango, Composio. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓ | api_versioning | No version segment in the base path (https://app.findymail.com/api, not /v1) and no explicit versioning/deprecation scheme documented. Docs last updated 2026-06-30. | [link](https://app.findymail.com/docs/) _primary_ | 2026-07-01 |
| ✓ | regional_availability | All data stored and processed exclusively in the EU (servers managed by Hetzner Online GmbH, located in Finland). Email finding is global; phone data is US-focused and explicitly excludes EU contacts due to GDPR. | [link](https://www.findymail.com/gdpr/) _primary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 Type 2 stated as certified on the Findymail homepage. Vendor self-attestation only — no public trust-center report, auditor, or report date located; not referenced in the DPA. | [link](https://www.findymail.com/) _primary_ | 2026-07-01 |
| ○ | iso27001 | No ISO 27001 certification mentioned on homepage, GDPR page, or DPA. | — |  |
| ✓ | gdpr | GDPR compliant. Acts as processor for user-imported data (Art. 28 DPA) and as controller for service delivery/fraud/compliance; lawful basis is legitimate interest in easier access to already-public business data; EU-hosted; EU and UK Standard Contractual Clauses incorporated in the DPA; opt-out / … | [link](https://www.findymail.com/gdpr/) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA referenced in the Data Processing Agreement; Findymail certifies it does not sell personal information and uses data only for the specified service purposes. | [link](https://www.findymail.com/dpa/) _primary_ | 2026-07-01 |

- **Waterfall placement:** WORK_EMAIL (find + verify): Place FIRST / early in the North America + global business-email waterfall. Rationale (ADR-0007 reservation-value): pay-only-on-hit credits (1 credit) plus real-time SMTP verification and a <5% bounce guarantee make its effective reservation value low — calling it first costs nothing on a miss and returns an already-verified address, minimizing downstream verification spend. EMAIL VERIFICATION: also usable as a standalone real-time verification layer (Verifier credits) to re-validate addresses returned by cheaper/less-trusted upstream providers before send. DIRECT_DIAL / MOBILE_PHONE: place as a US-only FALLBACK (10 credits, LinkedIn-URL input, explicitly excludes EU) — do NOT route EU phone requests here; prefer a dedicated phone waterfall (e.g. specialized mobile providers) ahead of it and skip Findymail entirely for EU numbers. TECHNOGRAPHICS / COMPANY firmographics (employee_count, industry, company_domain) and LINKEDIN_URL (reverse-email): PARALLEL/secondary enrichment, not a primary firmographic source. EU data-residency (EU-only hosting) is a plus for EU-processing pipelines. Bulk throughput: since there is no dedicated bulk match/verify endpoint, drive volume via the 300-concurrency limit with a client-side concurrency pool rather than expecting a batch call.

### Snov.io
- **Category:** business-email finder + verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** work_email, email_status, job_title, company_domain, industry, employee_count, linkedin_url
- **Summary:** Snov.io is a legitimate API-first business-email finder and email verifier (not fundamentally scraping-based). It exposes a documented REST API at https://api.snov.io/v2/ with OAuth2 client-credentials auth (apiUserId/apiSecret exchanged for a 1-hour Bearer token), async task-based batch endpoints (max 10 items/request), pagination, and webhooks. Core enrichment fields: work email + email status/SMTP validity, first/last name, job title/position, company domain, industry, and employee count; a li-profiles-by-urls endpoint also enriches from LinkedIn URLs. Pricing is a flexible credit model (1…
- **Adversarial verify:** 8 sampled → 8 confirmed, 0 downgraded. All three source URLs resolved and returned real Snov.io documentation; no fabricated or mismatched citations. Pages co…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | OAuth 2.0 client_credentials grant: POST apiUserId/apiSecret (client_id/client_secret) to https://api.snov.io/v1/oauth/access_token, receive Bearer access_token valid 3600s, sent as Authorization: Bearer header | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | Yes — RESTful JSON API. Base URL https://api.snov.io/v2/ (v1 legacy endpoints still active) | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — batch endpoints (emails[], rows[], urls[]) with a max batch size of 10 items per request | [link](https://snov.io/knowledgebase/how-to-use-snov-io-api/) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes — asynchronous task pattern: POST /{method}/start returns a task_hash; results retrieved by polling GET endpoints (domain-search, email-verification, emails-by-domain-by-name, li-profiles-by-urls) | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ✓✓ | pagination | Yes — page / per_page query params (e.g. 20/50/100 per page) with total_count and pagination links in response meta | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes — optional webhook_url parameter on batch endpoints plus dedicated webhook management (POST add / GET list / PUT status / DELETE) for prospect and campaign events | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | 60 requests per minute (~1 rps), applied on all plans across Email Finder, Verifier, campaigns and prospect endpoints | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No explicit per-day API cap published (throughput bounded by 60 req/min and by monthly credit allotment) | — |  |
| ○ | concurrency | No documented concurrency/parallel-request limit beyond the 60 req/min throttle | — |  |
| ○ | latency_p50_ms | Not published (async task-based; no SLA figures disclosed) | — |  |
| ○ | latency_p95_ms | Not published | — |  |
| ○ | coverage | Marketed as an 'extensive' leads/email database but no specific contact/profile count is published on primary pages | — |  |
| ✓✓ | accuracy | Email verifier advertises '98+% accuracy' via a 7-tier verification process (syntax, gibberish, domain, MX, SMTP server, mailbox existence, catch-all); bounce rates 'as low as 1.72%' | [link](https://snov.io/email-verifier) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based, 1 credit per prospect/email search/verification (no charge if no valid email found). Plans: Trial free (50 credits) / Starter $39/mo (1,000) / Pro S $99/mo (5,000) / Pro M $189/mo (20,000) / Pro L $369/mo (50,000) / Ultra $738/mo (100,000); 25% annual discount. API & Webhooks included… | [link](https://snov.io/pricing) _primary_ | 2026-07-01 |
| ✓ | price_per_match | 1 credit per successful match; effective cost ≈ $0.0074/credit on Ultra ($738/100k) up to ≈ $0.039/credit on Starter ($39/1k) — Pro S ≈ $0.0198/credit | [link](https://snov.io/pricing) _primary_ | 2026-07-01 |
| ○ | error_codes | Responses use success:true/false plus result-status enums (smtp_status: valid/unknown/not_valid; unknown_status_reason: banned/catchall/connection_error/greylist); a formal HTTP error-code table is not clearly documented | — |  |
| ○ | retry_behavior | No documented retry/backoff guidance for 429/5xx responses | — |  |
| ✓ | sdks | No official SDKs — docs provide PHP and Python code examples only; third-party/community clients exist (e.g. HelgeSverre/snov-io PHP client) | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning: v1 (legacy) and v2 (current primary) coexist under https://api.snov.io/{v1\|v2}/ | [link](https://snov.io/api) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global SaaS; personal data stored via Amazon (AWS), MongoDB and Hetzner with most data on Amazon servers; specific data-center regions not published; EU/EEA Art.27 representative (Privacity GmbH) appointed | [link](https://snov.io/knowledgebase/where-does-snov-io-transfer-your-personal-data/) _primary_ | 2026-07-01 |
| ✓ | soc2 | No self-certification — Snov.io does not claim its own SOC 2; it states it engages partners/contractors 'who have implemented the requirements of the SOC 2 or ISO 27001 security protocols'. Security Center lists no SOC 2 report | [link](https://snov.io/knowledgebase/where-does-snov-io-transfer-your-personal-data/) _primary_ | 2026-07-01 |
| ✓ | iso27001 | No self-certification — Snov.io does not claim its own ISO 27001; only requires vendors to have implemented SOC 2 or ISO 27001 protocols | [link](https://snov.io/knowledgebase/where-does-snov-io-transfer-your-personal-data/) _primary_ | 2026-07-01 |
| ✓ | gdpr | Yes — 'Snov.io fully adheres to GDPR'; Data Processing Addendum based on EU Standard Contractual Clauses, encryption in transit and at rest, pseudonymisation, dedicated /gdpr and /dpa pages, EU Art.27 representative Privacity GmbH | [link](https://snov.io/security-center) _primary_ | 2026-07-01 |
| ✓ | ccpa | Yes — Snov.io states compliance with the California Consumer Privacy Act (CCPA) on its Security Center / privacy materials | [link](https://snov.io/security-center) _primary_ | 2026-07-01 |

- **Waterfall placement:** Mid-waterfall fallback/parallel for business email discovery, plus a dedicated email-verification stage. Per ADR-0007 reservation-value thinking: Snov.io's low effective per-credit cost at scale (≈$0.0074–$0.02/credit on Pro/Ultra) and no-charge-on-no-hit model make it a strong low-reservation-value fallback to run AFTER premium email finders (e.g. call it only when higher-precision providers miss), maximizing marginal fill without inflating cost-per-record. Use POST /v2/emails-by-domain-by-name (find work email by name+domain) and POST /v2/domain-search as the enrichment calls, and POST /v2/email-verification as a cheap verification/re-validation stage (98+% accuracy, 7-tier) for emails sourced elsewhere. Regionally it has usable global + EU coverage with a GDPR Art.27 EU representative, so it is safe to place in EU/EEA email-finding waterfalls. THROUGHPUT CAVEAT: the hard 60 req/min (~1 rps) cap plus 10-item max batch limits it to ~600 lookups/min — schedule it as an async/batch fallback, not a real-time first-hop provider for high-volume jobs. EXCLUSION CAVEAT (ADR-0002): do NOT wire in the LinkedIn-channel endpoints (li-profiles-by-urls / LinkedIn Automation) — those are a scraping/ToS gray area; restrict integration to the email-finder + verifier REST endpoints, which are a legitimate API. Fields it contributes to the waterfall: work_email + email_status (primary value), plus supporting job_title, company_domain, industry, employee_count. Deprioritize for personal_email, phone, technographics, intent, or funding data — it does not reliably provide those.

### Datagma
- **Category:** email + phone + enrichment (EU)
- **Status:** DEPRIORITIZED
- **Capabilities:** work_email, mobile_phone, linkedin_url, job_title, company_domain, employee_count, industry, technographics, funding_stage, email_status
- **Summary:** Datagma is a legitimate REST enrichment API (base https://gateway.datagma.net, GET /api/ingress/v2/full) for finding/verifying B2B work emails, mobile phones, and company firmographics, with a strong EU/French posture (SIREN company data, GDPR DPA, SCCs, CNIL, opt-out). Auth is an apiId+password pair passed as query params. Pricing is credit-subscription with a genuine no-find-no-charge model (1 credit=email, 30 credits=mobile) and API access on every plan including Free. It is NOT fundamentally scraping-based: core email is find+ZeroBounce verify, phones are aggregated from third-party servi…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. One citation-integrity problem: the 'coverage' claim cites the syncgtm.com Datagma review for '75+ data points' and 'Fr…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key pair passed as query params: apiId + password (GET); credentials issued at app.datagma.com/user-api | [link](https://datagmaapi.readme.io/reference/ingressservice_fullapiv2) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST over HTTPS, GET requests. Base URL https://gateway.datagma.net ; primary enrich path /api/ingress/v2/full (full endpoint https://gateway.datagma.net/api/ingress/v2/full) | [link](https://datagmaapi.readme.io/reference/ingressservice_fullapiv2) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | No dedicated bulk/batch API endpoint; enrichment is single-record real-time. Batch is achieved client-side by looping at up to 10 req/s. CSV bulk upload exists only in the web app, not the API. | [link](https://datagmaapi.readme.io/llms.txt) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | None. All calls are synchronous real-time lookups; no async/job-queue or callback-based batch endpoints are documented. | [link](https://datagmaapi.readme.io/llms.txt) _primary_ | 2026-07-01 |
| ✓✓ | pagination | No cursor/offset pagination. The 'Find People' search returns a maximum of 10 people per call (successful search 10 credits, empty search 1 credit). | [link](https://datagmaapi.readme.io/llms.txt) _primary_ | 2026-07-01 |
| ○ | webhooks | No webhook/push infrastructure documented. 'Job Change Detection' is a query/poll endpoint, not an event callback. | — |  |
| ✓✓ | rate_limit_rps | 10 requests per second (provider states operations are real-time with no database, so heavy batching is discouraged). Note: help-center page 403'd to automated fetch but the value is indexed from Datagma's own help center and corroborated by multiple reviews. | [link](http://help.datagma.com/en/articles/8582305-batch-rate-limit-in-api) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | Not published (only a 10 req/s throttle is documented; daily/monthly ceiling is governed by plan credit balance, not an API-call cap). | — |  |
| ○ | concurrency | Not documented as a separate limit; the 10 req/s cap is the only published throttle. | — |  |
| ○ | latency_p50_ms | Not published. Real-time third-party/web lookups imply multi-hundred-ms to multi-second responses, but no figure is cited. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ⚠↓ | coverage | Enriches each record with 75+ data points (person, company, financial/funding, website/tech); strongest coverage in EU/US/Western Europe, with French company data via SIREN registry. (75+ figure is third-party reported.) | [link](https://syncgtm.com/blog/datagma-review) _secondary_ | 2026-07-01 |
| ✓✓ | accuracy | All emails are verified via ZeroBounce (primary claim). Third-party reviews report ~70-80% email match rate for US/Western-European contacts (secondary). | [link](https://datagma.com/api/) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit subscription (monthly or annual, 20% annual discount) with genuine no-find-no-charge for email & phone. 1 credit = 1 email (with company+person enrichment), 30 credits = 1 mobile phone. Free plan (90 emails/3 phones per mo) up to Expert $249/mo (22,500 emails or 750 phones); Enterprise = lar… | [link](https://datagma.com/pricing/) _primary_ | 2026-07-01 |
| ✓ | price_per_match | Email = 1 credit (~$0.011-0.016/email depending on plan; e.g. Expert $249/22,500 = $0.0111). Mobile phone = 30 credits (~$0.33-0.49/phone; e.g. Expert $249/750 = $0.332). No charge when data not found (email/phone); empty Find-People search still costs 1 credit. | [link](https://datagma.com/pricing/) _primary_ | 2026-07-01 |
| ○ | error_codes | Not documented in the public API reference. | — |  |
| ○ | retry_behavior | Not documented (no published backoff/idempotency guidance; exceeding 10 req/s presumably throttled but not specified). | — |  |
| ✓ | sdks | No official language/client SDKs published. REST API plus no-code connectors: Make, Clay, Zapier, n8n, and WhatsApp integration. | [link](https://apps.make.com/datagma) _secondary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning; current enrich API is v2 (/api/ingress/v2/). Getting-started docs are labeled 'v1.0'. | [link](https://datagmaapi.readme.io/reference/ingressservice_fullapiv2) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global availability with an EU/French focus (GDPR, French SIREN company data, opt-out form). A dedicated 'Search By Email (outside EU)' endpoint indicates email search is restricted for EU subjects for GDPR reasons. Platform hosted on AWS (Amazon Web Services, US) per the privacy policy's sub-proce… | [link](https://datagma.com/privacy-policy/) _primary_ | 2026-07-01 |
| ○ | soc2 | No SOC 2 report or certification is claimed or published on the privacy/policy pages. | — |  |
| ○ | iso27001 | No ISO 27001 certification is claimed or published. | — |  |
| ✓ | gdpr | GDPR-compliant (EU Regulation 2016/679). Acts as both controller and processor; signs Standard Contractual Clauses (SCCs) for transfers; provides a Data Processing Agreement; honors data-subject rights (Arts. 12-23) and CNIL complaints; offers a website opt-out form. | [link](https://datagma.com/privacy-policy/) _primary_ | 2026-07-01 |
| ○ | ccpa | No CCPA/California privacy statement found in the privacy policy or trust pages. | — |  |

**Downgraded on re-check:** `coverage` (no: The cited syncgtm review does NOT support this value. It makes no mention of '75+ data po…)

- **Waterfall placement:** Fallback / parallel EU-region provider for work_email, mobile_phone, and company firmographics (esp. French/EU contacts via SIREN). Per ADR-0007 reservation-value thinking: Datagma's no-find-no-charge model (email & phone billed only on a hit) makes its expected marginal cost low, so it is a cheap coverage-adding fallback to fire AFTER US-centric primaries when they miss on EU/Western-European contacts (its ~70-80% EU email match adds incremental hits). Do NOT place it first in the US waterfall (lower US coverage, no SOC2/ISO, single-source, no bulk/async API, 10 req/s cap makes high-volume runs slow). Split by field: (1) work_email -> mid/late email fallback with ZeroBounce-verified output; (2) mobile_phone -> later-stage fallback only, because 30 credits/hit (~$0.33-0.49) is comparatively expensive versus dedicated phone vendors; (3) company/firmographics + funding -> useful EU/French enrichment fallback. HARD CONSTRAINT: keep the 'Get the full content of any LinkedIn Profile' endpoint disabled (LinkedIn ToS / ADR-0002 no-scraping). Reassess to ACTIVE-CANDIDATE if Datagma publishes SOC 2/ISO 27001 and a bulk/async API.

### Enrow
- **Category:** email finder + waterfall verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** work_email, email_status, mobile_phone, direct_dial, phone_status
- **Summary:** Enrow is a legitimate API-first, pay-per-valid-result email finder + email verifier + phone finder (EU/France-based). It exposes a documented REST/JSON API at https://api.enrow.io with an async submit-then-poll (or webhook) model, single and bulk (up to 5,000/batch) endpoints, API-key header auth, and up to ~50 req/s. Its differentiator is deliverability: an independent Dropcontact 2026 benchmark (20k contacts) put it 3rd of 15 on hit rate (40.9% effective enrichment) but LOWEST hard-bounce (2.3%). Pricing is pay-per-success (credit charged only when a valid/deliverable email is returned; mis…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. No outright fabricated citations: the four enrow.readme.io pages and the syncgtm review are real and substantively supp…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key passed in an HTTP request header (per-request). Error responses '401 Missing apikey' / '401 Invalid apikey' confirm header-based key auth; official MCP server uses env var ENROW_API_KEY. Exact header token name ('apikey') inferred from error strings, not explicitly labeled in fetched sample… | [link](https://enrow.readme.io/reference/find-single-email) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON over HTTPS. Base URL https://api.enrow.io. Core endpoints: POST /email/find/single, POST /email/verify/single, POST /phone/find/single, GET /account/info. Content-Type application/json. | [link](https://enrow.readme.io/reference/find-single-email) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes. Bulk endpoint POST /email/find/bulk (a.k.a. find/multiple), plus bulk verify and bulk phone. Max 5,000 searches per batch (docs: 'It can go up to 5000 searches per batch'; official MCP: 'up to 5,000' per batch). | [link](https://enrow.readme.io/reference/find-multiple-emails) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Fully asynchronous. Submit returns 201 with a search id + estimated_duration (in minutes); results retrieved by polling GET result-by-id (200 = finished, 202 = ongoing, 404 = not found) or via webhook callback on completion. | [link](https://enrow.readme.io/reference/get-single-email-finder-result) _primary_ | 2026-07-01 |
| ✓✓ | pagination | No cursor/offset pagination in the documented API. Results are retrieved per search id (async job model); bulk results fetched via a dedicated GET results endpoint keyed by batch id. | [link](https://enrow.readme.io/reference/get-single-email-finder-result) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes. Provide a 'webhook' URL in the request 'settings' (single and bulk); Enrow calls it the moment the search/bulk finishes, so you can skip the GET poll. Dedicated webhook-payload docs exist for email-finder, email-verifier, and phone-finder results. | [link](https://enrow.readme.io/reference/find-single-email) _primary_ | 2026-07-01 |
| ⚠↓ | rate_limit_rps | Up to 50 requests per second (stated as consistent across Enrow's APIs). NOTE: enrow.io/api/email-finder is bot-blocked to direct WebFetch (404); the '50 req/s' figure was confirmed via search retrieval of that primary Enrow page across multiple queries. | [link](https://enrow.io/api/email-finder) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No published per-day request cap; throughput is effectively bounded by credit balance and the ~50 req/s limit. | — |  |
| ○ | concurrency | No separately documented concurrent-job limit beyond the ~50 req/s throughput ceiling; bulk jobs process up to 5,000 searches server-side. | — |  |
| ○ | latency_p50_ms | Not published as a percentile. Enrow markets an ~2-second average search-to-verified-contact time, but that is a marketing average, not a measured p50. | — |  |
| ○ | latency_p95_ms | Not published. Async model with estimated_duration in minutes for bulk implies tail latency far above single-call figures; no p95 disclosed. | — |  |
| ✓✓ | coverage | Mid-pack. Independent Dropcontact 2026 benchmark (20,000 contacts): 40.9% effective enrichment rate, ranked 3rd of 15 tools. Enrow's own marketing claims 60%+ enrichment. Prioritizes valid results over max coverage. | [link](https://syncgtm.com/blog/enrow-review) _secondary_ | 2026-07-01 |
| ✓ | accuracy | Deliverability leader in cohort: 2.3% hard-bounce rate (lowest of 15 tools), 5.8% wrong-domain, 8.1% total error (Dropcontact 2026). Enrow markets <2% bounce and 0% catch-all leakage; verifier includes catch-all detection. | [link](https://syncgtm.com/blog/enrow-review) _secondary_ | 2026-07-01 |
| ✓ | pricing_model | Pay-per-valid-result (success-based): a credit is consumed only when a valid/deliverable email is returned; misses cost nothing; unused credits roll over. Unified credit system: Email Finder = 1 credit/result, Email Verifier = 0.25 credit/check, Phone Finder = 40 credits/result. Free tier: 50 credi… | [link](https://enrow.io/fr/tarifs) _primary_ | 2026-07-01 |
| ✓ | price_per_match | Per valid email: EUR0.015 (Start) down to EUR0.0072 (Scale); phone EUR0.60 -> EUR0.29; verify EUR0.00375 -> EUR0.0018. Third-party review states ~$0.012/valid email (Starter) and ~$0.005 (Enterprise 200k+). | [link](https://enrow.io/fr/tarifs) _primary_ | 2026-07-01 |
| ✓ | error_codes | Documented: 201 (bulk accepted), 200 (result ready), 202 (search ongoing), 400 (invalid JSON / missing company info / missing fullname), 401 (missing/invalid apikey), 404 (search id not found), 422 (insufficient balance). | [link](https://enrow.readme.io/reference/find-single-email) _primary_ | 2026-07-01 |
| ○ | retry_behavior | No documented client retry guidance or webhook redelivery/retry policy; 202 'ongoing' status implies caller-driven polling until 200. | — |  |
| ✓ | sdks | No official language SDK packages. ReadMe docs provide copy-paste code samples in Shell, Node, Ruby, PHP, and Python. Official MCP server published (EnrowAPI/enrow-mcp) exposing 14 tools (find/verify/phone, single + bulk, account). | [link](https://github.com/EnrowAPI/enrow-mcp) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Docs header labels the API 'v1.0'. No version segment in the URL path (endpoints are api.enrow.io/email/... with no /v1/). No documented deprecation policy. | [link](https://enrow.readme.io/reference/find-single-email) _primary_ | 2026-07-01 |
| ✓ | regional_availability | EU-headquartered provider (France; French-language site and pricing). API served globally over https://api.enrow.io; supports optional country_code hint per search. No documented data-residency / region-selection or multi-region hosting. | [link](https://enrow.io/en) _primary_ | 2026-07-01 |
| ○ | soc2 | No evidence of SOC 2 Type I/II attestation on the site or trust materials. | — |  |
| ○ | iso27001 | No evidence of ISO/IEC 27001 certification found. | — |  |
| ✓ | gdpr | Enrow states it is 'fully aligned with European data protection laws' (GDPR). EU-based operator strengthens plausibility, though no DPA/certificate document was fetched. | [link](https://enrow.io/en) _primary_ | 2026-07-01 |
| ✓ | ccpa | Enrow states 'California privacy standards met' (CCPA alignment) on its site. | [link](https://enrow.io/en) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `rate_limit_rps` (unreachable: Cited source returns HTTP 404 to direct fetch (bot-blocked, as the claim itself concedes)…)

- **Waterfall placement:** FIRST-position email finder for deliverability-sensitive lanes (EU/EMEA and US), and simultaneously the verification gate for the email waterfall. ADR-0007 reservation-value rationale: pay-per-valid pricing (misses are free) plus the lowest-in-cohort 2.3% hard-bounce rate means Enrow has near-zero cost and near-zero deliverability risk on a miss, giving it high reservation value as the first probe for work_email. Its ~41% effective coverage is only mid-pack, so it MUST be backed by higher-coverage fallbacks (e.g. FullEnrich ~48%, Datagma) to catch its misses. Use Enrow's Email Verifier (0.25 credit, catch-all detection) as the shared verification stage that gates all upstream finders. Phone Finder is expensive (40 credits/result) with LinkedIn-URL input, so place it as a FALLBACK / last-resort step for mobile_phone and direct_dial, never as the first phone provider. Best regional fit: EU/France-origin data and GDPR/CCPA-aligned, so favor Enrow early in the ordering for EU/EMEA contacts.

### Anymail Finder (anymailfinder.com)
- **Category:** business-email finder
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** work_email, email_status, job_title, linkedin_url, company_domain
- **Summary:** Anymail Finder is a legitimate API-first B2B email-finding and verification service (operating since 2016, ~250M requests/month per its marketing). It is NOT a LinkedIn/data-scraping reseller: it finds business (work) email addresses from a person name + company/domain, a LinkedIn URL, or a domain, then live-verifies deliverability via SMTP before charging. Core offering is a clean REST/JSON API over HTTPS (base https://api.anymailfinder.com, URL-path versioned v5.1) with API-key auth, an async bulk API (up to 100,000 rows/job), and optional webhooks. Pricing is pay-only-for-verified: you are…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All 8 source_urls are real, load, and are on-topic (no fabricated citations). One overstatement caught: the webhooks cl…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key passed in the 'Authorization' request header (raw key as the header value, not a Bearer prefix). Key managed at newapp.anymailfinder.com/settings/api. | [link](https://anymailfinder.com/email-finder-api/docs/authentication) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | Yes. REST over HTTPS returning JSON. Base URL https://api.anymailfinder.com. Example: POST /v5.1/find-email/person (also /find-email/decision-maker, /find-email/company, /find-email/linkedin, /verify-email). | [link](https://anymailfinder.com/email-finder-api/docs/find-person-email) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes. Async bulk email search accepting up to 100,000 rows in a single job (JSON or multipart file upload, e.g. POST /v5.1/bulk/multipart). Avg ~1,000 rows / 5 minutes. | [link](https://anymailfinder.com/email-finder-api/docs/bulk) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes. Bulk jobs are processed asynchronously; poll GET /v5.1/bulk/{searchId} for status or receive a webhook, then GET /v5.0/bulk/{searchId}/download. Credits are charged only when completed results are downloaded. | [link](https://anymailfinder.com/email-finder-api/docs/bulk) _primary_ | 2026-07-01 |
| ○ | pagination | No pagination scheme documented; single-search endpoints return one result object and bulk results are retrieved as a single downloadable file. Not published. | — |  |
| ⚠↓ | webhooks | Yes. Optional async delivery by adding an 'x-webhook-url' header to a request (supported on person, company, decision-maker, LinkedIn, verify-email, and bulk). Fired when the search/verification completes. Payload schema, signing, and delivery-retry behavior are NOT documented. | [link](https://anymailfinder.com/email-finder-api/docs/webhook) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | No published per-second/RPS cap. Provider explicitly markets 'no rate limits', 'no throttling' and 'no 429s to handle'. Treat concurrency limits as unknown despite this claim. | [link](https://anymailfinder.com/email-finder-api) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_day | No daily API call caps per the docs ('No daily API call caps, so you can focus on building'). Usage is bounded by purchased credits (billed only on verified results), not by request count. | [link](https://anymailfinder.com/email-finder-api/docs) _primary_ | 2026-07-01 |
| ○ | concurrency | No concurrency / parallel-request limit documented. | — |  |
| ○ | latency_p50_ms | No p50 percentile published. Provider markets ~8.1s average single-search response and '5-15 seconds typical' (SMTP-dependent) — an average/range, not a p50 percentile, so left unverified. | — |  |
| ○ | latency_p95_ms | No p95 latency percentile published. | — |  |
| ✓✓ | coverage | Marketed 86.4% verified coverage (measured on a 5,000 B2B contact test set). B2B/work-email only. | [link](https://anymailfinder.com/email-finder-api) _primary_ | 2026-07-01 |
| ✓ | accuracy | Marketed 98.9% verified accuracy ('confirmed by three independent external verifiers'), plus a 97%+ delivery guarantee with credit-back for hard bounces. Vendor self-reported. | [link](https://anymailfinder.com/email-finder-api) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based monthly/annual subscription with pay-only-for-verified billing: charged only when a deliverable ('valid') email is found; risky/not-found results are free. Person email=1 credit, company emails=1 credit (up to 20), decision-maker=2 credits, verify-email=0.2 credit. Duplicate searches w… | [link](https://anymailfinder.com/pricing) _primary_ | 2026-07-01 |
| ✓ | price_per_match | Per-credit (≈ per verified person-email) price scales by tier: $29/mo=400cr ($0.073), $49/1k ($0.049), $89/2k ($0.045), $149/5k ($0.030), $199/10k ($0.020), $299/25k ($0.012), $499/50k ($0.010), $799/100k ($0.008). Annual plans ~33% cheaper. Decision-maker = 2x; verification = 0.2 credit. | [link](https://anymailfinder.com/pricing) _primary_ | 2026-07-01 |
| ✓ | error_codes | Documented HTTP codes: 200 OK (returned once search completes), 400 Bad Request (missing/malformed data), 401 Unauthorized (missing/invalid API key), 402 Payment Needed (insufficient credits). No 429 documented (consistent with 'no rate limits' claim). | [link](https://anymailfinder.com/email-finder-api/docs/find-person-email) _primary_ | 2026-07-01 |
| ○ | retry_behavior | No client retry/backoff guidance or webhook redelivery policy documented. (A commercial credit-back / 97%+ delivery guarantee exists but is billing remediation, not API retry semantics.) | — |  |
| ✓ | sdks | No official language SDK libraries published in the docs; positioned as a raw REST API callable 'from any language' plus no-code integrations (n8n, Make, Zapier, Clay). | [link](https://anymailfinder.com/email-finder-api) _primary_ | 2026-07-01 |
| ✓ | api_versioning | URL-path versioning. Current major version v5.1 (e.g. /v5.1/find-email/person, /v5.1/verify-email, /v5.1/bulk/...); bulk download endpoint still on /v5.0/bulk/{searchId}/download. | [link](https://anymailfinder.com/email-finder-api/docs/find-person-email) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global API access; no customer-selectable data region documented. Infrastructure hosted on AWS (United States) and Hetzner Online GmbH (Germany); international transfers governed by EU Standard Contractual Clauses + AWS DPA. | [link](https://anymailfinder.com/legal/gdpr) _primary_ | 2026-07-01 |
| ○ | soc2 | No SOC 2 (Type I/II) report or attestation mentioned anywhere in the legal/GDPR or docs pages reviewed. | — |  |
| ○ | iso27001 | No ISO/IEC 27001 certification mentioned in any reviewed page. | — |  |
| ✓ | gdpr | GDPR-compliant posture: acts as processor for customer-uploaded data (controller for account/billing), offers a DPA defining Article 28 obligations, appointed a DPO, uses EU SCCs for transfers, publishes a subprocessor list (AWS, Hetzner, Crisp, Plausible, Google, Slack, Rewardful, Sentry, Postmark… | [link](https://anymailfinder.com/legal/gdpr) _primary_ | 2026-07-01 |
| ○ | ccpa | No CCPA/CPRA-specific commitment or 'do not sell/share' statement found on the GDPR/legal page reviewed. | — |  |

**Downgraded on re-check:** `webhooks` (no: Contradicted in part. Page confirms the x-webhook-url header, that it fires on completion…)

- **Waterfall placement:** Work_email discovery/verification, B2B only. Per ADR-0007 reservation-value thinking, place EARLY (first or near-first) in the work_email waterfall: pay-only-for-verified pricing means a miss costs zero, so its effective reservation value / expected cost when it fails is ~0, making it cheap to try first before more expensive always-charge providers. Strong for both EU and US targets (data resident in AWS-US and Hetzner-Germany, GDPR DPA + SCCs available), best when the input key is name+company/domain or a LinkedIn URL. Use its /v5.1/verify-email endpoint (0.2 credit) as a validation stage over emails sourced elsewhere in the waterfall, and its async bulk API (100k rows/job, webhook on completion) for batch backfills. Do NOT use it for phone numbers, personal (non-work) emails, or firmographic enrichment — it returns only business emails plus light LinkedIn-sourced person metadata (name, title, LinkedIn URL, company). Reserve decision-maker search (2 credits) for role-targeting when a specific person name is unknown.

### ZeroBounce
- **Category:** email verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** email_status, work_email, company_domain
- **Summary:** ZeroBounce is a legitimate API-first email verification provider (not a scraper), fully compatible with our API-first engine (ADR-0002). It exposes a well-documented REST v2 API with API-key auth, single-email GET validation, a synchronous batch endpoint (max 100 emails/request), and a fully asynchronous bulk-file endpoint with an optional return_url webhook callback. Regional processing endpoints exist for US (api-us) and EU (api-eu), which supports GDPR data-residency needs. Compliance posture is strong: SOC 2 Type 2, ISO/IEC 27001:2013, GDPR, CCPA, HIPAA, PCI, and EU-U.S./Swiss-U.S. Data P…
- **Adversarial verify:** 8 sampled → 6 confirmed, 2 downgraded. Two of eight claims downgraded. (1) bulk_api: the cited URL is the api-rate-limits page, which supports only the valida…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key passed as URL query parameter (api_key); requires SSL/HTTPS | [link](https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-validate-emails) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON API. Single validate: GET https://api.zerobounce.net/v2/validate (regional: https://api-us.zerobounce.net/v2/validate, https://api-eu.zerobounce.net/v2/validate). Returns status, sub_status, account, domain, mx_found, mx_record, smtp_provider, did_you_mean, firstname, lastname, gender, ci… | [link](https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-validate-emails) _primary_ | 2026-07-01 |
| ⚠↓ | bulk_api | Yes. Synchronous batch endpoint POST https://api.zerobounce.net/v2/validatebatch accepts an email_batch JSON array; max 100 emails per request (docs recommend the bulk-file API above 200 emails). Response returns email_batch + errors arrays. | [link](https://www.zerobounce.net/docs/api-dashboard/api-rate-limits) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes. Asynchronous bulk-file API: POST https://bulkapi.zerobounce.net/v2/sendfile (multipart/form-data), poll via filestatus (file_status/file_phase_2_status: Queued/Processing/Complete), retrieve via getfile. No restriction on file size or number of emails. | [link](https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-send-file/) _primary_ | 2026-07-01 |
| ○ | pagination | No cursor/offset pagination documented; async bulk results are retrieved as a whole file via getfile after completion. | — |  |
| ✓✓ | webhooks | Yes (async bulk only). sendfile accepts optional return_url parameter: 'The URL will be used to call back when the validation is completed.' No webhooks for real-time single/batch validation. | [link](https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-send-file/) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | Single validate: 80,000 requests / 10 seconds standard (100,000 for ZeroBounce ONE) ~= 8,000 rps; exceeding = 1-minute block. Batch validate: 30 requests/min standard (40 for ONE); exceeding = 10-minute cooling-off. | [link](https://www.zerobounce.net/docs/api-dashboard/api-rate-limits) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No explicit per-day validation cap published; validation limits are expressed per-10-seconds. (Ancillary endpoints: GetCredits 80k/hour, GetAPIUsage 800/hour.) | — |  |
| ○ | concurrency | No explicit concurrent-connection limit published; quickstart states the single endpoint 'can be called asynchronously and is not currently rate-limited' but no numeric concurrency figure is given. | — |  |
| ○ | latency_p50_ms | Percentile latency not published. Docs cite single-validate response time of ~1-30 seconds on average (not a p50). | — |  |
| ○ | latency_p95_ms | Percentile latency not published. | — |  |
| ○ | coverage | Global email validation coverage; no published numeric match/coverage rate. Optional append/activity_data adds name, gender, and geo fields when available. | — |  |
| ✓✓ | accuracy | '99.6% Validation Accuracy Guaranteed' advertised across all pricing tiers. | [link](https://www.zerobounce.net/email-validation-pricing) _primary_ | 2026-07-01 |
| ⚠↓ | pricing_model | Credit-based. 1 credit per email verified (bulk or real-time API); unknown results are free (never consume a credit). Options: pay-as-you-go (min 2,000 credits) and ZeroBounce ONE monthly subscription; free tier 100 credits/month. Email Finder costs 20 credits per successful query. | [link](https://www.zerobounce.net/email-validation-pricing) _primary_ | 2026-07-01 |
| ✓ | price_per_match | ZeroBounce ONE: 10,000 credits for $99/mo (~$0.0099 per verified email; annual billing saves 20%). Pay-as-you-go starts at 2,000 credits with volume discounts. Email Finder: 20 credits (~$0.20+) per successful match. | [link](https://www.zerobounce.net/email-validation-pricing) _primary_ | 2026-07-01 |
| ✓ | error_codes | Validation results returned via status taxonomy (valid, invalid, catch-all, unknown, spamtrap, abuse, do_not_mail) plus granular sub_status; batch endpoint returns an 'errors' array alongside results. Bad API key requests (200+/hour) trigger a temporary block. | [link](https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-batch-validate-emails) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | Rate-limit overages produce temporary blocks/backoff windows: single validate = 1-minute block, batch = 10-minute cooling-off, bad API key = 1-hour block. Unknown results consume no credit and can be safely re-validated. | [link](https://www.zerobounce.net/docs/api-dashboard/api-rate-limits) _primary_ | 2026-07-01 |
| ✓ | sdks | Official open-source SDK wrappers for PHP, Python, Java, C#/.NET, and Rust hosted on the ZeroBounce GitHub org. | [link](https://github.com/zerobounce) _primary_ | 2026-07-01 |
| ✓ | api_versioning | URL-path versioning; current version is v2 (all endpoints under /v2/, e.g. /v2/validate, /v2/validatebatch, /v2/sendfile). | [link](https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-validate-emails) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Regional processing endpoints: default api.zerobounce.net, US api-us.zerobounce.net, EU api-eu.zerobounce.net (single, batch, and getapiusage). Bulk file API at bulkapi.zerobounce.net. Enables EU-region processing for data residency. | [link](https://www.zerobounce.net/docs/email-validation-api-quickstart/v2-validate-emails) _primary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 Type 2 certified as of March 22, 2022 (initial SOC 2 compliance effective July 9, 2021), audited in three-month intervals against the 5 Trust Services Criteria. | [link](https://www.zerobounce.net/docs/about-zerobounce/certifications-and-accreditations) _primary_ | 2026-07-01 |
| ✓ | iso27001 | ISO/IEC 27001:2013 certified; attained MSECB Management System Certificate on August 24, 2022. (Provider newsroom also references ISO 27001:2022 recertification.) | [link](https://www.zerobounce.net/docs/about-zerobounce/certifications-and-accreditations) _primary_ | 2026-07-01 |
| ✓ | gdpr | EU GDPR compliant (since before the May 25, 2018 enforcement date); operates as a data processor with a Data Protection Officer and third-party audits. Active in EU-U.S./Swiss-U.S. Data Privacy Framework. | [link](https://www.zerobounce.net/docs/about-zerobounce/certifications-and-accreditations) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA compliant (listed among compliance frameworks; dedicated California privacy page at zerobounce.net/ca-privacy.html). | [link](https://www.zerobounce.net/security) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `bulk_api` (no: The cited rate-limits page corroborates only the endpoint URL (api.zerobounce.net/v2/vali…); `pricing_model` (no: Mostly correct: credit-based (1 credit/email, bulk or API), unknown results free, free ti…)

- **Waterfall placement:** Terminal verification stage. Per ADR-0007 reservation-value thinking, ZeroBounce has high reservation value as the email-verification/validation step and low value as a discovery source. Place it LAST in the waterfall as the email_status verifier that scores/gates candidate work_email + personal_email values produced by upstream discovery/enrichment providers before delivery, rather than as a first- or parallel-position contact source. Route EU-subject records to the api-eu.zerobounce.net endpoint for GDPR data residency and US records to api-us.zerobounce.net. Its Email Finder (20 credits/query) could serve as a fallback (last-resort) work_email discovery source, but that is secondary to its primary verification role. Applies to all regions for the email_status field; not recommended for phone, job_title, firmographic, or intent fields.

### NeverBounce (ZoomInfo)
- **Category:** email verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** email_status
- **Summary:** NeverBounce (a ZoomInfo company) is a legitimate API-first email verification / list-hygiene provider — NOT a scraper (ADR-0002 compliant). It exposes a RESTful HTTPS/JSON API (base https://api.neverbounce.com/v4, current v4.2) with a real-time single-check endpoint and an asynchronous bulk jobs pipeline (create -> parse -> verify -> paginated results, with callback_url webhooks). It returns a normalized deliverability status (valid / invalid / disposable / catchall / unknown) via SMTP/MX/syntax/disposable/catch-all detection. Auth is a static API key (secret_...). Well-documented rate/thrott…
- **Adversarial verify:** 8 sampled → 8 confirmed, 0 downgraded. All four cited URLs are genuine developers.neverbounce.com reference pages that loaded and contained on-topic content; …

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Static API key, format secret_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx (V4). Passed via query string (key=), form-urlencoded body, or JSON body. Server-side only; V3 credentials incompatible (return auth_failure). | [link](https://developers.neverbounce.com/reference/authentication) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | RESTful service over HTTPS returning JSON. Base URL https://api.neverbounce.com/v4 (current v4.2). Endpoints incl. /single/check, /jobs/create, /jobs/results, /account/info. | [link](https://developers.neverbounce.com/reference/jobs-create) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes. POST /v4.2/jobs/create accepts supplied_data (JSON array of emails) or remote_url (hosted CSV via HTTP Basic Auth/FTP). Max request body 25MB (413 Entity Too Large if exceeded); recommend up to ~1M emails per job, chunk larger lists. | [link](https://developers.neverbounce.com/reference/usage-guidelines) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes. Bulk jobs are asynchronous: parse then verify (auto_parse/auto_start/run_sample flags); completion pushed via callback_url or fetched via /jobs/results by job_id. | [link](https://developers.neverbounce.com/reference/jobs-create) _primary_ | 2026-07-01 |
| ✓✓ | pagination | GET /v4.2/jobs/results supports page (default 1) and items_per_page (1-1000, default 10) for paginated result retrieval by job_id. | [link](https://developers.neverbounce.com/reference/jobs-results) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes. /jobs/create accepts optional callback_url plus callback_headers for job-lifecycle event notifications. | [link](https://developers.neverbounce.com/reference/jobs-create) _primary_ | 2026-07-01 |
| ○ | rate_limit_rps | No per-second RPS limit published. API applies burst throttling and returns throttle_triggered when exceeded; exact numeric RPS not documented. | — | 2026-07-01 |
| ✓✓ | rate_limit_day | Max 50 job runs per day; do not create more than 10 jobs per 100k items per hour (exceptions available on request). | [link](https://developers.neverbounce.com/reference/usage-guidelines) _primary_ | 2026-07-01 |
| ✓✓ | concurrency | Maximum 10 concurrent bulk jobs per account. | [link](https://developers.neverbounce.com/reference/usage-guidelines) _primary_ | 2026-07-01 |
| ○ | latency_p50_ms | Not published by provider. | — | 2026-07-01 |
| ○ | latency_p95_ms | Not published by provider. | — | 2026-07-01 |
| ○ | coverage | No published coverage %; it is a verification provider (validates candidate emails), not an enrichment data source. Single-check returns valid/invalid/disposable/catchall/unknown. | — | 2026-07-01 |
| ✓ | accuracy | Vendor-claimed 'up to 99.9%' bounce removal with a 97% deliverability money-back guarantee (refund if >3% bounce after cleaning); independent review observed ~93% real-world accuracy. | [link](https://www.usebouncer.com/neverbounce-review/) _secondary_ | 2026-07-01 |
| ✓ | pricing_model | Pay-as-you-go credit model (1 credit = 1 verification), volume-tiered; plus monthly subscription tiers. Credits expire (~12 months). Enterprise custom pricing. Official pricing page returned HTTP 403 to automated fetch; figures from fetched secondary review. | [link](https://www.usebouncer.com/neverbounce-api/) _secondary_ | 2026-07-01 |
| ✓ | price_per_match | ~$0.008/email for smaller lists, dropping to ~$0.002/email at ~2M emails (~$0.003 at 1M+). (Official pricing page 403; secondary-sourced.) | [link](https://www.usebouncer.com/neverbounce-api/) _secondary_ | 2026-07-01 |
| ✓ | error_codes | status field in 2xx responses: success, general_failure, auth_failure, temp_unavail, throttle_triggered, bad_referrer. Also standard HTTP 4xx/5xx incl. 413 Entity Too Large for oversized job payloads. | [link](https://developers.neverbounce.com/reference/error-handling) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | On throttle_triggered: retry shortly / adjust rate-limit settings. On 5xx/temp_unavail: retry the request and check API status. Client-side retry recommended; no documented automatic exponential backoff. | [link](https://developers.neverbounce.com/reference/error-handling) _primary_ | 2026-07-01 |
| ✓ | sdks | Official SDKs: PHP, Ruby, Node.js (v5, TypeScript support), Python, Go, .NET, Java. | [link](https://developers.neverbounce.com/docs/api-getting-started) _primary_ | 2026-07-01 |
| ✓ | api_versioning | URL-path versioned. Current major V4 (latest v4.2; v4.0/v4.1 also live); legacy V3 (incompatible auth schema). e.g. /v4.2/single/check. | [link](https://developers.neverbounce.com/reference/getting-started) _primary_ | 2026-07-01 |
| ○ | regional_availability | No data-residency / regional endpoint options published; US-based (ZoomInfo). SMTP-based verification is region-agnostic in practice. | — | 2026-07-01 |
| ✓ | soc2 | Parent ZoomInfo is SOC 2 Type II certified (AICPA). Encryption at rest AES256, in transit TLS 1.2+. | [link](https://www.zoominfo.com/legal/security-overview) _primary_ | 2026-07-01 |
| ✓ | iso27001 | Parent ZoomInfo certified ISO 27001 and ISO 27701 (privacy). | [link](https://www.zoominfo.com/legal/security-overview) _primary_ | 2026-07-01 |
| ✓ | gdpr | ZoomInfo holds TrustArc GDPR Practices Validation and TRUSTe Enterprise Privacy Certification; NeverBounce markets a GDPR/CCPA data-privacy compliance suite. | [link](https://pipeline.zoominfo.com/marketing/trustarc-privacy-validations) _secondary_ | 2026-07-01 |
| ✓ | ccpa | ZoomInfo earned TrustArc GDPR/CCPA privacy validations; NeverBounce provides a GDPR/CCPA data-privacy compliance suite. | [link](https://pipeline.zoominfo.com/marketing/trustarc-privacy-validations) _secondary_ | 2026-07-01 |

- **Waterfall placement:** Verification layer, not a sourcing layer. NeverBounce does not discover contact data; it validates the deliverability of candidate emails (returns email_status: valid/invalid/disposable/catchall/unknown). Per ADR-0007 reservation-value thinking, place it as the terminal deliverability gate applied to work_email and personal_email AFTER the email-discovery enrichment waterfall resolves candidates — and as the tie-breaker when multiple enrichment providers return competing email candidates (accept the address NeverBounce marks 'valid'; drop 'invalid'/'disposable'). Two integration modes: (1) real-time GET /single/check with a timeout param for on-demand/single-record flows; (2) async POST /jobs/create for batch list hygiene, with callback_url webhooks + paginated /jobs/results for large backfills. Region: global/region-agnostic (SMTP-based), US-based vendor; no data-residency options, so route EU-resident PII validation with a DPA in place. It is a fallback/finisher in ordering — never first or parallel for sourcing — because it produces no new fields, only a confidence signal on emails already in hand.

### Kickbox
- **Category:** email verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** email_status
- **Summary:** Kickbox is a legitimate API-first email verification provider (not a scraper) that validates deliverability of a given email address and returns a result (deliverable/undeliverable/risky/unknown) plus quality signals. It exposes a versioned REST/HTTPS JSON API (base https://api.kickbox.com/v2) with a real-time single-verify GET endpoint and an asynchronous PUT /v2/verify-batch endpoint (with completion callback) for bulk/CSV lists. Auth is via an apikey parameter; official SDKs exist for PHP, Ruby, Python, and Node.js. It is a verification/validation layer, not an enrichment or contact-discov…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All three cited URLs are reachable and rendered real, relevant content; none appear fabricated. Caveat: two of the thre…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ⚠↓ | auth | API key. Passed as `apikey` parameter; also accepted via authorization header. Client initialized with a single API key (e.g. new Kickbox\Client('API_KEY')). | [link](https://github.com/kickboxio/kickbox-php) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | Yes — REST/HTTPS JSON. Base URL https://api.kickbox.com/v2. Single real-time verify: GET https://api.kickbox.com/v2/verify?apikey=...&email=... | [link](https://www.zerobounce.net/docs/api-migration/kickbox) _secondary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — asynchronous batch/CSV upload via PUT https://api.kickbox.com/v2/verify-batch (returns job id); status/results via GET https://api.kickbox.com/v2/verify-batch?jobid=...&apikey=... Maximum batch/list size not publicly documented (UNVERIFIED). | [link](https://www.zerobounce.net/docs/api-migration/kickbox) _secondary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes — verify-batch is asynchronous: PUT returns a job id and success flag; results polled by jobid or delivered via callback. Supports X-Kickbox-Callback and X-Kickbox-Filename headers. | [link](https://www.zerobounce.net/docs/api-migration/kickbox) _secondary_ | 2026-07-01 |
| ○ | pagination | No cursor/offset pagination documented; single verify returns one record, batch results retrieved as a whole by jobid. | — |  |
| ✓✓ | webhooks | Batch completion callback URL supported via X-Kickbox-Callback header (webhook-style notification on job completion). No general event webhooks documented. | [link](https://www.zerobounce.net/docs/api-migration/kickbox) _secondary_ | 2026-07-01 |
| ○ | rate_limit_rps | Not published in accessible primary docs. | — |  |
| ○ | rate_limit_day | Not published in accessible primary docs. | — |  |
| ○ | concurrency | Not published in accessible primary docs. | — |  |
| ○ | latency_p50_ms | Not published. (verify accepts a `timeout` param, default 6000ms, and returns an X-Kickbox-Response-Time header, but no p50 latency figure is disclosed.) | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ○ | coverage | No coverage/answer-rate percentage published; provider markets 'over 5 billion emails verified' (cumulative volume, not a coverage rate). | — |  |
| ○ | accuracy | No verified accuracy percentage from primary source. Billing model (charges only for deliverable/undeliverable, risky/unknown free) implies confidence gating but is not an accuracy metric. | — |  |
| ✓✓ | pricing_model | Transparent pay-as-you-go credits (no subscription required); 100 free verifications for new accounts. Published tiers from $5/500 up to $4,000/1,000,000; volume discounts beyond 1M by contact. | [link](https://www.usebouncer.com/kickbox-pricing/) _secondary_ | 2026-07-01 |
| ✓✓ | price_per_match | ~$0.01 per verification at entry ($5 / 500) scaling down to ~$0.004 per verification at 1M ($4,000 / 1,000,000). | [link](https://www.usebouncer.com/kickbox-pricing/) _secondary_ | 2026-07-01 |
| ✓✓ | error_codes | Response carries `success` (bool) and `message` (nullable) plus a `reason` enum: invalid_email, invalid_domain, rejected_email, accepted_email, low_quality, low_deliverability, no_connect, timeout, invalid_smtp, unavailable_smtp, unexpected_error. `result` enum: deliverable/undeliverable/risky/unkn… | [link](https://github.com/kickboxio/kickbox-php) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | No explicit server-side retry policy published. Client controls a `timeout` param (default 6000ms); on timeout the API returns result=unknown / reason=timeout, signalling caller to retry. Balance returned via X-Kickbox-Balance header. | [link](https://github.com/kickboxio/kickbox-php) _primary_ | 2026-07-01 |
| ✓ | sdks | Official SDK client libraries maintained by the kickboxio org: PHP, Ruby, Python, and Node.js. | [link](https://github.com/orgs/kickboxio/repositories) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning; current major version is v2 (all endpoints under /v2/, e.g. /v2/verify, /v2/verify-batch). | [link](https://www.zerobounce.net/docs/api-migration/kickbox) _secondary_ | 2026-07-01 |
| ○ | regional_availability | No documented multi-region hosting or data-residency options; single API host api.kickbox.com (treat as US-hosted). | — |  |
| ✓ | soc2 | Listed as SOC 2 compliant on a third-party security profile (not confirmed against Kickbox's own trust page, which was inaccessible). | [link](https://security-profiles.nudgesecurity.com/app/kickbox-com) _secondary_ | 2026-07-01 |
| ✓ | iso27001 | Listed as ISO 27001 compliant on a third-party security profile. | [link](https://security-profiles.nudgesecurity.com/app/kickbox-com) _secondary_ | 2026-07-01 |
| ✓ | gdpr | Listed as GDPR compliant on a third-party security profile. | [link](https://security-profiles.nudgesecurity.com/app/kickbox-com) _secondary_ | 2026-07-01 |
| ○ | ccpa | Not mentioned on the security profile reviewed; could not verify a CCPA attestation. | — |  |

**Downgraded on re-check:** `auth` (no: Page loads and confirms only the client-init part: `$client = new Kickbox\Client('Your_AP…)

- **Waterfall placement:** Verification/validation layer for the email_status field only — placed AFTER email-discovery providers as a deliverability gate before an email is emitted or a downstream match is billed (ADR-0007 reservation-value gating). First-choice or parallel real-time verifier (GET /v2/verify) for work_email in US/global; async bulk verifier (PUT /v2/verify-batch) for list-hygiene. Does not participate in the contact-discovery waterfall (returns no phones/titles/firmographics/LinkedIn). US-hosted; no documented EU residency.

### Emailable
- **Category:** email verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** email_status
- **Summary:** Emailable (emailable.com, formerly Blaze Verify) is a legitimate API-first email verification/validation provider — not a scraper and not a contact-discovery database. It performs real-time syntax, MX and SMTP-level mailbox checks and returns a deliverability state (deliverable / undeliverable / risky / unknown) plus enrichment flags (disposable, role, free, accept_all/catch-all, no_reply, mailbox_full, did_you_mean typo suggestion) and light name parsing. Clean REST API at https://api.emailable.com/v1/ with URI versioning (v1), API-key and OAuth2 auth, official Ruby/Node/Python SDKs, a synch…
- **Adversarial verify:** 8 sampled → 8 confirmed, 0 downgraded. No fabricated citations detected. All four source URLs loaded and genuinely support their claims. The only minor discre…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key or OAuth2 access token. Key passed via api_key query param OR 'Authorization: Bearer <key>' header (also POST body). Keys are prefixed test_ (simulated, no credits) or live_; private (server-side, all endpoints, IP allowlist) vs public (client-side, /verify only, requires trusted-domain whi… | [link](https://emailable.com/docs/api/authentication/) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON API. Base URL https://api.emailable.com/v1/. Single verification: GET /v1/verify?email=... (optional smtp=true/false, timeout 2-10s default 5). | [link](https://emailable.com/docs/api/emails/) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes. POST /v1/batch accepts up to 50,000 emails per batch. | [link](https://emailable.com/docs/api/emails/) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes, asynchronous. POST /v1/batch returns a batch id; poll status/results via GET /v1/batch?id=<id>. | [link](https://emailable.com/docs/api/emails/) _primary_ | 2026-07-01 |
| ○ | pagination | No cursor/offset pagination documented; batch results are returned in full via the batch status endpoint or callback payload. | — |  |
| ✓✓ | webhooks | Yes (batch callback). Provide a URL to POST /v1/batch; on completion Emailable sends results via HTTP POST (JSON identical to batch status). If the endpoint does not return HTTP 200, delivery is retried hourly for up to 3 days. | [link](https://emailable.com/docs/api/emails/) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | Per-endpoint, per-second: /v1/verify = 25/sec, /v1/batch = 5/sec, /v1/account = 5/sec. Exceeding returns HTTP 429 'Rate Limit Exceeded' with ratelimit-limit / ratelimit-remaining / ratelimit-reset headers. Enterprise plans get custom higher limits. | [link](https://emailable.com/docs/api/rate-limits/) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_day | No overall per-day cap documented for standard keys (throttling is per-second). Public (client-side) keys are limited to 10 unique requests per day per user. | [link](https://emailable.com/docs/api/authentication/) _primary_ | 2026-07-01 |
| ○ | concurrency | No explicit concurrency/parallel-connection limit published; throughput is governed by the per-second rate limits above. | — |  |
| ○ | latency_p50_ms | Not published as a percentile SLA. Docs show a single example response with duration 0.127s and marketing cites ~30k/min bulk throughput, but no p50 is stated. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ✓✓ | coverage | Global, provider-agnostic verification via MX/SMTP (not a static database match); detects catch-all/accept_all, disposable, role, free, no_reply and mailbox_full. Vendor reports 15B+ verifications processed for 300,000+ customers. | [link](https://emailable.com/api/) _primary_ | 2026-07-01 |
| ✓ | accuracy | 99%+ deliverability guarantee on all providers (97%+ on Microsoft-managed mailboxes e.g. Office 365/Outlook/Hotmail), applicable only to addresses returned 'Deliverable', sent within 24h, opt-in, min 1,000 unique addresses. Third-party reviews report ~90-95% in real-world catch-all cases. | [link](https://emailable.com/guarantee/) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based, 1 credit per verification (bulk, API, verifier, widget). Pay-as-you-go plus subscription (subscribe & save 15%). 250 free credits on signup; minimum paid purchase 5,000 credits; credits never expire; 'unknown' results are free/refunded; premium add-ons priced separately (Inbox Reports… | [link](https://emailable.com/pricing/) _primary_ | 2026-07-01 |
| ○ | price_per_match | Exact per-credit USD prices did not render on the primary pricing page (volume calculator failed to load). Secondary reviews cite roughly $0.004/verification PAYG down to ~$0.00135 at high volume, but figures conflict — not verified from a fetched primary source. | — |  |
| ✓ | error_codes | Documented status codes: 249 Try Again (transient, resend), 400 Bad Request, 401 Unauthorized (no API key), 402 Payment Required (insufficient credits), 403 Forbidden (invalid key/auth/captcha), 404 Not Found, 429 Too Many Requests, 500 Internal Server Error, 503 Service Unavailable (maintenance). | [link](https://emailable.com/docs/api/status-codes/) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | 249 'Try Again' explicitly instructs the client to resend the request; 429 responses include ratelimit-reset to back off. Batch callbacks are retried hourly for up to 3 days until the receiver returns HTTP 200. No automatic client-side retry/backoff is performed by the API itself. | [link](https://emailable.com/docs/api/status-codes/) _primary_ | 2026-07-01 |
| ✓ | sdks | Official SDKs: Ruby (gem install emailable), Node.js (npm/yarn/pnpm add emailable), Python (pip/uv add emailable). Plain HTTP for other languages. | [link](https://emailable.com/docs/api/) _primary_ | 2026-07-01 |
| ✓ | api_versioning | URI path versioning; current version v1 (all endpoints under /v1/). | [link](https://emailable.com/docs/api/) _primary_ | 2026-07-01 |
| ○ | regional_availability | No EU/US data-residency or regional endpoint options documented; US-based service, single global API host. Not verified. | — |  |
| ✓ | soc2 | SOC 2 Type II certified (stated on API page and site footer badge linking to trust center). | [link](https://emailable.com/api/) _primary_ | 2026-07-01 |
| ○ | iso27001 | Not advertised; no ISO 27001 claim found on API, privacy, or marketing pages. | — |  |
| ✓ | gdpr | GDPR compliant — privacy policy states data is processed under EU 2016/679 GDPR with data-subject rights (access, rectification, erasure, portability); a Data Processing Agreement is offered (updated 2024-06-06). | [link](https://emailable.com/privacy-policy/) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA/CPRA compliant — dedicated California section granting access/correction/opt-out/deletion rights; explicitly states 'We do not sell personal information, as defined in the CCPA.' | [link](https://emailable.com/privacy-policy/) _primary_ | 2026-07-01 |

- **Waterfall placement:** VALIDATION stage, not discovery. Emailable is a verification-only service (returns email_status), so it does not compete with contact-discovery providers in the enrichment waterfall — it sits after them as the deliverability gate. Recommended placement: terminal email-validation step applied to any work_email / personal_email produced by upstream discovery providers, before a candidate email is accepted/returned. Per ADR-0007 reservation-value thinking, use real-time GET /v1/verify (25 rps) inline for single-record enrichment paths and POST /v1/batch (up to 50k, async callback) for backfill/list-cleaning jobs. Region-agnostic (SMTP-based, global coverage), so it can serve as the first-choice verifier for all regions given its SOC 2 Type II + GDPR/CCPA posture and 99%/97% deliverability guarantee; otherwise run in parallel with a second verifier (e.g., ZeroBounce/NeverBounce) and reconcile 'risky/unknown' states. Not used for phone_status or any phone/social/company fields. Note pricing must be confirmed with sales before finalizing waterfall cost ordering (price_per_match UNVERIFIED).

### Twilio Lookup
- **Category:** phone verification / line-type + carrier
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** phone_status
- **Summary:** Twilio Lookup is a first-party, API-first phone number intelligence service (not a scraper) that validates a supplied E.164 number and returns line type (mobile/landline/fixed/non-fixed VoIP/tollFree/etc.), carrier name, MCC/MNC, plus optional add-on data packages (Caller Name/CNAM, SIM Swap, Call Forwarding, Line Status, Identity Match, Reassigned Number, SMS Pumping Risk). It is a synchronous REST GET on lookups.twilio.com/v2, HTTP Basic auth (API key/secret or Account SID/Auth Token), pay-as-you-go per request ($0.008/Line Type Intelligence request), worldwide coverage (Canada by approval)…
- **Adversarial verify:** 8 sampled → 8 confirmed, 0 downgraded. All three cited URLs are genuine Twilio documentation pages and loaded successfully; none appear fabricated. Claims are…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | HTTP Basic auth: API key SID as username + API key secret as password (Account SID + Auth Token also work for local testing) | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST GET https://lookups.twilio.com/v2/PhoneNumbers/{PhoneNumber}; JSON response; base fields (valid, nationalFormat, countryCode, callingCountryCode) always returned, data packages via Fields query param | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | No dedicated bulk/batch endpoint; Lookup v2 accepts a single phone number per GET request (fan out client-side) | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | No asynchronous batch/job API documented; Lookup is a synchronous request/response call | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓✓ | pagination | N/A — single-resource GET on one phone number; no list/collection endpoint, so no pagination | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | No webhooks/callbacks for Lookup; result returned synchronously in the HTTP response body | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | No fixed public per-second limit; Twilio states limits 'vary depending on the requested package' and downstream providers 'do not have uniform rate limits' (exceeding returns error 60616). No numeric RPS published. | [link](https://www.twilio.com/docs/api/errors/60616) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No published daily request quota for Lookup | — |  |
| ✓✓ | concurrency | Account-level concurrency limit enforced across REST APIs; exceeding returns HTTP 429 Too Many Requests (safely retryable). Current usage exposed via Twilio-Concurrent-Requests response header. No fixed numeric concurrency value published for Lookup. | [link](https://www.twilio.com/docs/usage/rest-api-best-practices) _primary_ | 2026-07-01 |
| ○ | latency_p50_ms | Not published by Twilio | — |  |
| ○ | latency_p95_ms | Not published by Twilio | — |  |
| ✓ | coverage | Line Type Intelligence 'is available for phone numbers worldwide'; Canadian phone numbers require special approval (querying without access returns error 60601). Carrier data not available for personal/tollFree/premium/sharedCost/uan/voicemail/pager/unknown line types (null carrier fields). | [link](https://www.twilio.com/docs/lookup/v2-api/line-type-intelligence) _primary_ | 2026-07-01 |
| ○ | accuracy | No published accuracy/match-rate percentage for line type or carrier data | — |  |
| ✓ | pricing_model | Pay-as-you-go, billed per request; basic formatting/validation is free, premium data packages billed per request (Line Status is volume-tiered) | [link](https://www.twilio.com/en-us/user-authentication-identity/pricing/lookup) _primary_ | 2026-07-01 |
| ✓ | price_per_match | Line Type Intelligence $0.008/request; Line Status $0.007 (0-100K) down to $0.00385 (>10M); Inbound caller identification (Caller Name/CNAM) $0.01/request; Identity Match $0.10/request (varies by country); SMS Pumping Risk $0.025/request rest-of-world (complimentary in NAMER); SIM Swap & Call Forwa… | [link](https://www.twilio.com/en-us/user-authentication-identity/pricing/lookup) _primary_ | 2026-07-01 |
| ✓ | error_codes | Documented codes: 60601 (Canada line-type access not enabled), 60616 (Lookup rate limit exceeded), HTTP 429 (account concurrency limit). Per-package errors also surfaced in each data package's error_code field (e.g. line_type_intelligence.error_code). | [link](https://www.twilio.com/docs/api/errors/60616) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | Twilio recommends 'implementing retries with exponential backoff'; 429 Too Many Requests means the request was not processed and 'you can safely retry'. No Lookup-specific idempotency key documented. | [link](https://www.twilio.com/docs/usage/rest-api-best-practices) _primary_ | 2026-07-01 |
| ✓ | sdks | Official helper libraries: Node.js, Python, C#/.NET, Java, Go, PHP, Ruby; plus Twilio CLI and curl examples | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning; v2 is current (/v2/PhoneNumbers). v1 is deprecated with a published V1->V2 migration guide. | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Data-residency regions US1 (default) and IE1 (Ireland/EU); number coverage worldwide (see coverage). Some packages region-scoped (Caller Name US-only, Call Forwarding UK-only, Reassigned Number US-only). | [link](https://www.twilio.com/docs/lookup/v2-api) _primary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 Type 2 attestation covering 'All Services' per Twilio's certification matrix | [link](https://www.twilio.com/en-us/legal/security-overview) _primary_ | 2026-07-01 |
| ✓ | iso27001 | ISO/IEC 27001 certified; also holds ISO/IEC 27017 and ISO/IEC 27018 (cloud security and PII protection), covering Twilio Services and Segment Services | [link](https://www.twilio.com/en-us/legal/security-overview) _primary_ | 2026-07-01 |
| ✓ | gdpr | 'Twilio is committed to ensuring that our platform is GDPR-compliant.' Provides a Data Protection Addendum, Binding Corporate Rules (BCRs) for intra-group transfers, and applies GDPR-grade Data Protection by Design to all data globally. | [link](https://www.twilio.com/en-us/gdpr) _primary_ | 2026-07-01 |
| ✓ | ccpa | DPA Schedule 4 addresses CCPA 'as amended by the California Privacy Rights Act'; 'Twilio certifies that it understands and will comply with its obligations under the CCPA', will not sell/share customer personal information, and imposes CCPA-compliant terms on sub-processors. | [link](https://www.twilio.com/en-us/legal/twilio-ccpa-notice) _primary_ | 2026-07-01 |

- **Waterfall placement:** Fits the phone_status field only (line-type + carrier + validity); it does not return emails, names, or firmographics. Recommended placement: FIRST/PRIMARY verifier for phone_status on global/international mobile candidates, given documented worldwide coverage and data-residency options (US1 default, IE1 for EU data). Per ADR-0007 reservation-value thinking: Twilio's free formatting/validation should run as the cheap gate on every phone_status candidate, and paid Line Type Intelligence ($0.008/req) is a high-reservation-value primary for confirming a number is a live mobile (SMS-reachable) with carrier/MCC/MNC before downstream dialing/SMS spend. For US-centric line-type/carrier where a cheaper vendor exists, place Twilio in PARALLEL or as FALLBACK to minimize per-request cost; for non-US/global numbers Twilio is the preferred first hop. SMS Pumping Risk (free in NAMER) is a useful complimentary risk signal to co-request in the NAMER region. Not applicable to email/identity waterfalls. No numeric latency/RPS SLAs are published, so orchestration should treat it as synchronous with client-side concurrency control and exponential-backoff retries on HTTP 429 (ADR-0004/0008 timeout+retry budgeting).

### Telnyx Number Lookup
- **Category:** phone verification / carrier
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** phone_status, carrier_name, line_type, ported_status, cnam_caller_name, mobile_country_code_mnc
- **Summary:** Telnyx Number Lookup is a legitimate, first-party REST API (not a scraper) for phone number intelligence: line-type/validity, carrier (LRN + MCC/MNC), portability (SPID/OCN, ported_status), and CNAM caller-name. It is a synchronous single-number GET (GET https://api.telnyx.com/v2/number_lookup/{phone_number}) authenticated with a Bearer V2 API key, with SDKs in Python/Ruby/Node/PHP/Java/.NET. Pricing is transparent pay-as-you-go per query (LRN $0.0015, carrier MCC/MNC $0.0025, CNAM $0.003). Compliance posture is strong (SOC 2 Type I/II/III, ISO 27001:2013, GDPR, CCPA), though those certificat…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All four source URLs were reachable and returned content. Caveat: the batch_async and pagination claims are cited to an…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | HTTP Bearer token — Telnyx V2 API key sent as 'Authorization: Bearer YOUR_API_KEY' | [link](https://developers.telnyx.com/docs/identity/number-lookup/quickstart) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON. GET https://api.telnyx.com/v2/number_lookup/{phone_number} (base URL https://api.telnyx.com/v2). Optional query param type=carrier\|caller-name. Returns data{ record_type, phone_number(E.164), national_format, country_code, carrier{name,type,mobile_country_code,mobile_network_code}, port… | [link](https://developers.telnyx.com/docs/identity/number-lookup/quickstart) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | No bulk/batch Lookup endpoint — the API is a single-number GET (phone_number in path). A separate self-service Portal number-portability CSV tool checks up to 2,500 numbers at a time, but that is not the programmatic Lookup API. | [link](https://telnyx.com/products/number-lookup) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Synchronous request/response (200 returns the record inline). No asynchronous batch-job API is documented for Number Lookup. | [link](https://preview.redoc.ly/telnyx/FILE-557-Storage-Docs-Update/openapi/spec/tag/Number-Lookup-API/) _primary_ | 2026-07-01 |
| ✓✓ | pagination | None — a single record is returned per lookup (no list/paged response). | [link](https://preview.redoc.ly/telnyx/FILE-557-Storage-Docs-Update/openapi/spec/tag/Number-Lookup-API/) _primary_ | 2026-07-01 |
| ○ | webhooks | No webhook/callback documented for Number Lookup (response is synchronous). Not verified. | — |  |
| ○ | rate_limit_rps | Rate limiting is enforced (429 / error code 10011 'Too many requests') and exposed via x-ratelimit-limit/remaining/reset headers, but Telnyx does not publish a numeric RPS for Number Lookup ('limits are static but subject to change'; contact support). | — |  |
| ○ | rate_limit_day | No per-day request cap published for Number Lookup. | — |  |
| ○ | concurrency | No documented concurrency limit for the Number Lookup API. | — |  |
| ○ | latency_p50_ms | Not published by Telnyx. | — |  |
| ○ | latency_p95_ms | Not published by Telnyx. | — |  |
| ⚠↓ | coverage | CNAM caller-name sourced from the US nationally-maintained CNAM database (real-time); carrier/LRN + portability strongest for North American (NANP) numbers. International number lookup is referenced (product FAQ) but a per-country coverage list is not enumerated. | [link](https://telnyx.com/products/number-lookup) _primary_ | 2026-07-01 |
| ○ | accuracy | No published accuracy/match-rate metric for Number Lookup. | — |  |
| ✓✓ | pricing_model | Pay-as-you-go, charged per query; volume/monthly-commit contracts offer discounted rates below PAYG. | [link](https://telnyx.com/pricing/number-lookup) _primary_ | 2026-07-01 |
| ✓✓ | price_per_match | LRN $0.0015/query; MCC/MNC carrier lookup $0.0025/query; CNAM caller-name $0.003/query; inbound CNAM (auto-display) $0.40/number/month; outbound CNAM listing free. | [link](https://telnyx.com/pricing/number-lookup) _primary_ | 2026-07-01 |
| ✓ | error_codes | 200 OK; 422 Unprocessable Entity; 429 Too many requests (Telnyx error code 10011); 'default' unexpected-error object with code/title/detail. | [link](https://preview.redoc.ly/telnyx/FILE-557-Storage-Docs-Update/openapi/spec/tag/Number-Lookup-API/) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | On 429, Telnyx recommends exponential backoff; x-ratelimit-limit/remaining/reset response headers indicate consumption and reset window; response caching advised to reduce request frequency. | [link](https://developers.telnyx.com/development/api-fundamentals/reliability/rate-limiting) _primary_ | 2026-07-01 |
| ✓ | sdks | Official SDKs: Python, Ruby, Node.js, PHP, Java, .NET (plus curl examples). | [link](https://preview.redoc.ly/telnyx/FILE-557-Storage-Docs-Update/openapi/spec/tag/Number-Lookup-API/) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning: v2 (https://api.telnyx.com/v2/...). | [link](https://developers.telnyx.com/docs/identity/number-lookup/quickstart) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global API endpoint (api.telnyx.com). EU/data-residency: 'Data Locality' one-time choice for where CDRs/MDRs are stored at rest; GDPR data-transfer mechanisms in place for transfers outside the EU. CNAM data is US-based. | [link](https://telnyx.com/data-privacy) _primary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 Type I (Security & Availability) and SOC 2 Type 2 & Type 3 (Security, Confidentiality & Availability), documented for Programmable Voice, Programmable Messaging, Video and Wireless (platform scope; full reports via trust.telnyx.com under NDA). | [link](https://telnyx.com/legal/technical-organizational-security-practices) _primary_ | 2026-07-01 |
| ✓ | iso27001 | ISO 27001:2013 certified for Programmable Voice, Programmable Messaging, Video and Wireless. | [link](https://telnyx.com/legal/technical-organizational-security-practices) _primary_ | 2026-07-01 |
| ✓ | gdpr | GDPR-aligned: commits to appropriate cross-border data-transfer mechanisms, security measures, breach notification, DPIAs, offers a DPA, and assists with data-subject requests. | [link](https://telnyx.com/data-privacy) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA/CPRA-aligned: supports California resident rights (access, opt-out of sale, deletion, portability, non-discrimination) and commits to ongoing California data-privacy compliance. | [link](https://telnyx.com/data-privacy) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `coverage` (no: Page supports two sub-claims: the 'nationally-maintained CNAM database' wording and the i…)

- **Waterfall placement:** Phone-verification/carrier lane, not email/person enrichment. Per ADR-0007 reservation-value: very low per-query cost ($0.0015-$0.003) plus strong NANP/US coverage and real-time US CNAM give it a high reservation value for US/North-America phone_status + line_type + carrier validation, so place FIRST/primary for US number validation and carrier/line-type, and as the primary source for CNAM caller-name (US national CNAM DB). Run PARALLEL with (or FALLBACK to) Twilio Lookup for cross-verification and for international numbers where Telnyx coverage is not enumerated. Do NOT use it in the email/LinkedIn/person lanes — it returns no work_email/personal_email/job_title. Synchronous single-number GET, so batch it with client-side concurrency (bounded, since numeric RPS is unpublished) rather than expecting a bulk endpoint.

### Kaspr
- **Category:** mobile + direct-dial (EU)
- **Status:** DEPRIORITIZED _(research agent said: EXCLUDED — Fundamentally LinkedIn-scraping-based: the primary API endpoint (POST /profile/linkedin) requires a LinkedIn profile URL as input and the d…; Reclassified per ADR-0009: LinkedIn-extension provenance → compliance-gated, human policy confirmation pending (was EXCLUDED by research agent).)_
- **Capabilities:** mobile_phone, direct_dial, work_email, personal_email, job_title, company_domain
- **Summary:** Kaspr is an EU-focused (France/UK/Spain hubs) B2B contact provider offering mobile numbers, direct dials and emails, with a documented REST API (base https://api.developers.kaspr.io). However its data acquisition is fundamentally LinkedIn-scraping-based: the core product is a Chrome extension that scrapes LinkedIn, and the API's primary enrichment endpoint (POST /profile/linkedin) REQUIRES a LinkedIn profile URL as input. The French SA (CNIL), per the EDPB, fined Kaspr EUR 200,000 in 2025 for unlawful scraping of LinkedIn contact details (including users who restricted visibility), no lawful …
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All 8 source_urls loaded and returned relevant content; no fabricated/dead citations among the provided URLs. One integ…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key passed in the 'Authorization' header (raw key, not Bearer); Content-Type: application/json; header must specify API v2. Key obtained at app.kaspr.io/settings/api. | [link](https://help.kaspr.io/en/articles/8857583-how-to-setup-kaspr-api) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON API. Base URL https://api.developers.kaspr.io. Primary endpoint POST /profile/linkedin enriches by LinkedIn profile URL (Sales Navigator URLs not supported). | [link](https://help.kaspr.io/en/articles/8857583-how-to-setup-kaspr-api) _primary_ | 2026-07-01 |
| ○ | bulk_api | No bulk/batch API endpoint found in accessible docs; API processes individual profile requests. CSV/Excel enrichment is a UI feature, not an API batch endpoint. Full Stoplight reference was inaccessible for confirmation. | — |  |
| ○ | batch_async | No async/batch job API documented in accessible sources. | — |  |
| ○ | pagination | Not documented in accessible sources (enrichment is single-profile lookup). | — |  |
| ○ | webhooks | No webhook/callback support documented in accessible sources. | — |  |
| ✓✓ | rate_limit_rps | Business plan advertises 'Advanced API access (60 requests per minute)' = ~1 request/sec. Lower tiers not specified. | [link](https://help.kaspr.io/en/articles/8800588-kaspr-pricing-and-subscription-plans) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | Setup docs state 'request limits exist per hour/day based on subscription plan' but no specific daily numeric cap is published; effective daily ceiling is governed by monthly credit allowances, not a published RPD limit. | — |  |
| ○ | concurrency | No concurrency limit documented in accessible sources. | — |  |
| ○ | latency_p50_ms | Not published. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ⚠↓ | coverage | Marketed as '200 million+ European B2B contacts' (kaspr.io/our-data); regulator (EDPB/CNIL) cites a database of ~160 million contacts. EU-specialized. Built from 120+ sources incl. LinkedIn, registries (Whois), GitHub, business directories. | [link](https://www.kaspr.io/our-data) _primary_ | 2026-07-01 |
| ✓✓ | accuracy | No formal accuracy SLA published. Emails put through multi-step verification and re-validated ~every 2 months; vendor cites G2 user testimonials ('95% of the time right') rather than an audited accuracy metric. | [link](https://www.kaspr.io/our-data) _primary_ | 2026-07-01 |
| ✓✓ | pricing_model | Credit-based SaaS subscription with separate credit pools (Phone / Direct Email / Export). Free: 5 phone + 5 direct-email + 10 export credits/mo, unlimited B2B emails. Starter EUR 59/mo (EUR 45/mo annual): 100 phone + 5 direct-email + 1,000 export/mo. Business EUR 99/mo (EUR 79/mo annual): 200 phon… | [link](https://help.kaspr.io/en/articles/8800588-kaspr-pricing-and-subscription-plans) _primary_ | 2026-07-01 |
| ✓✓ | price_per_match | No fixed USD/EUR per-match price. Consumption-based in credits: one export credit per successful API call, plus phone/email credits per data type fetched. Effective per-match cost varies by plan/credit bundle. | [link](https://www.kaspr.io/api) _primary_ | 2026-07-01 |
| ○ | error_codes | Error/status code catalog not available in accessible docs (full Stoplight/developer reference could not be fetched). | — |  |
| ○ | retry_behavior | No documented retry/backoff or idempotency guidance in accessible sources. | — |  |
| ○ | sdks | No official SDKs/client libraries found; integration is raw REST (docs hosted on Stoplight). | — |  |
| ✓✓ | api_versioning | API v2; version selected via request header ('specify the v2 of the API'). | [link](https://help.kaspr.io/en/articles/8857583-how-to-setup-kaspr-api) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Europe-focused data (hubs in France, UK, Spain); claims global B2B coverage but core strength and DB are EU. No stated regional API endpoint/hosting choices. | [link](https://www.kaspr.io/our-data) _primary_ | 2026-07-01 |
| ○ | soc2 | No SOC 2 certification stated on the privacy/trust pages; not found in any primary source. | — |  |
| ○ | iso27001 | No ISO 27001 certification stated on the privacy/trust pages; not found in any primary source. | — |  |
| ✓ | gdpr | Vendor claims GDPR-aligned via legitimate interest (Art. 6.1(f)). CONTESTED: the French SA (CNIL), per the EDPB, fined Kaspr EUR 200,000 in 2025 for unlawful LinkedIn scraping (incl. profiles with restricted visibility), no lawful basis (Art. 6), disproportionate ~5-yr retention (Art. 5), and trans… | [link](https://www.edpb.europa.eu/news/news/2025/data-scraping-french-sa-fined-kaspr-eu200-000_en) _primary_ | 2026-07-01 |
| ✓ | ccpa | Vendor claims CCPA-aligned (database 'GDPR and CCPA aligned'); customers remain responsible for local compliance. No independent CCPA certification/audit found. | [link](https://www.kaspr.io/api) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `coverage` (no: Page supports the headline figure ('Unlock 200 million+ European B2B contacts'), '120+ ve…)

- **Waterfall placement:** Do not place in the waterfall — EXCLUDED. Absent the compliance issues it would have been a last-resort FALLBACK tier for EU (FR/UK/ES) mobile + direct-dial enrichment keyed on a LinkedIn URL, sitting behind compliant EU phone providers. Under ADR-0007 reservation-value thinking its expected compliant value is negative (LinkedIn-ToS + GDPR enforcement risk), so it should not be reserved a slot for any field or region. If ever reconsidered, only as a manually legal-reviewed, last-resort EU mobile/direct-dial fallback — not for email.

### ContactOut
- **Category:** business + personal email + phone (linkedin-centric)
- **Status:** DEPRIORITIZED _(research agent said: EXCLUDED — Fundamentally scraping/crawling-based data provenance + LinkedIn-reseller ToS/legal risk, conflicting with ADR-0002 (engine is API-first, n…; Reclassified per ADR-0009: LinkedIn/crawl provenance → compliance-gated, human policy confirmation pending (was EXCLUDED by research agent).)_
- **Capabilities:** work_email, personal_email, mobile_phone, direct_dial, linkedin_url, job_title, company_domain, employee_count, industry, technographics, funding_stage, email_status
- **Summary:** ContactOut is a LinkedIn-centric email/phone finder that DOES expose a legitimate server-side REST API (token auth, v1/v2, bulk batch up to 1000, async job_id + webhooks, documented rate limits and error codes) — so it is technically integrable in an API-first engine. However, it is EXCLUDED under ADR-0002 (API-first, no scraping): its dataset is fundamentally built by web crawling/scraping (ContactOut's own our-data page: "We scan the entire internet" and servers that "crawl billions of webpages daily"), and its flagship product is a LinkedIn-scraping Chrome extension. As a LinkedIn data res…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. Both source URLs are genuine and resolve to the real ContactOut API docs and data pages; no fabricated or mismatched ci…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key passed in HTTP header named 'token' (format: token: <YOUR_API_TOKEN>). Keys issued via sales/demo request; no OAuth. | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | Yes — JSON REST API. Base URL https://api.contactout.com. Endpoints incl. /v1/linkedin/enrich (GET), /v1/email/enrich (GET), /v1/people/enrich (POST), /v1/people/search (POST), /v1/company/search (POST), /v1/domain/enrich (POST). | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — batch endpoints. /v1/people/linkedin/batch max 100 profiles (synchronous); /v2/people/linkedin/batch max 1000 profiles (asynchronous). | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes — v2 batch returns status QUEUED plus a UUID job_id; retrieve via polling GET /v2/people/linkedin/batch/{job_id} or via callback_url webhook. v2 also does real-time work-email guess+verify if not in DB. | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓✓ | pagination | Yes — page and page_size parameters. People Search default page_size 25, maximum 25. | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes — optional callback_url; on async batch completion ContactOut POSTs the results to that URL. | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | No per-second limit published. Documented per-minute caps: People Search API 60 req/min (~1 rps); Contact Checker APIs 150 req/min (~2.5 rps); all other APIs 1000 req/min (~16.7 rps). | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No per-day API rate limit published (throughput governed by per-minute caps + credit/plan quotas; e.g. free tier is 5 credits/day, not an API-tier daily rate limit). | — |  |
| ○ | concurrency | No documented concurrency/parallel-request limit. | — |  |
| ○ | latency_p50_ms | Not published. Note: v2 real-time work-email guess+verify and async batch imply variable latency, but no figures are cited. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ⚠↓ | coverage | 800M people profiles, 150M personal emails, 200M work emails, 100M direct dials, 40M company profiles (vendor-stated). Global with strong US/UK; US/UK data is a premium tier. | [link](https://contactout.com/our-data) _primary_ | 2026-07-01 |
| ✓ | accuracy | Vendor-stated 'Triple-verified with 99% Confidence' for emails/phones; hourly data updates. Self-reported marketing claim, no third-party benchmark cited. | [link](https://contactout.com/our-data) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based subscription. Individual: Free (5 emails/5 phones/5 exports per day), Email $49/mo (~300 exports/mo), Email+Phone $99/mo (~600 exports/mo); Sales tier advertised 'unlimited sales users for $299'; Team/API tier is custom, 'Contact Us' (700M+ bulk license, Salesforce/ATS). API credit con… | [link](https://contactout.com/pricing) _primary_ | 2026-07-01 |
| ○ | price_per_match | No published dollar price per match/credit for the API; Team/API pricing is custom (contact sales). Consumption is 1 credit per found email/phone, but per-credit $ cost is not disclosed. | — |  |
| ✓ | error_codes | HTTP status codes documented: 400 Bad Request (invalid params), 401 Unauthorized, 403 Forbidden, 404 Not Found, 422 Unprocessable Entity, 429 Too Many Requests, 500 Internal Server Error. | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | 429 Too Many Requests returned when a per-minute rate limit is exceeded. No Retry-After header, backoff schedule, or documented retry guidance beyond the stated limits. | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ○ | sdks | No official language SDKs published in the API docs. A community Postman collection and third-party integrations (Pipedream, Unified.to) exist but are not first-party SDKs. | — |  |
| ✓ | api_versioning | Yes — path-based versioning; both v1 and v2 endpoints exist (e.g. /v1/linkedin/enrich, /v2/people/linkedin/batch). v2 adds real-time work-email finder and async batch (max 1000). | [link](https://api.contactout.com/) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global data coverage; US/UK data is a distinct premium option (pricing page toggle 'Exclude US/UK Data 50% off' vs 'Include US/UK Data'), indicating strongest coverage/pricing weight on US/UK. No documented regional API endpoints or data-residency options. | [link](https://contactout.com/pricing) _primary_ | 2026-07-01 |
| ✓ | soc2 | Self-stated 'SOC 2 Certified' on marketing pages. No SOC 2 Type I/II report or trust-portal link provided; treat as self-reported, unverified by third-party attestation link. | [link](https://contactout.com/api-feature) _primary_ | 2026-07-01 |
| ○ | iso27001 | ContactOut does NOT claim ISO 27001 for itself; ISO 27001 appears only in reference to its underlying AWS infrastructure. No ContactOut ISO 27001 certification stated. | — |  |
| ✓ | gdpr | Self-stated 'GDPR Compliant' (compliant with GDPR/CCPA and USA privacy laws per marketing/privacy pages). | [link](https://contactout.com/api-feature) _primary_ | 2026-07-01 |
| ✓ | ccpa | Self-stated 'CCPA Compliant' (California consumer privacy compliance). | [link](https://contactout.com/api-feature) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `coverage` (no: All five numbers are verbatim-supported on the page (800M People Profiles, 150M Personal …)

- **Waterfall placement:** EXCLUDED, so not placed in production ordering. If provenance/ToS were resolved (compliant licensing), reservation-value thinking (ADR-0007) would place it EARLY/near-first in the LinkedIn-URL-keyed personal-contact waterfall for US/UK: strongest for personal_email and mobile_phone/direct_dial when a LinkedIn profile URL is the input key, with v2 adding real-time work-email guess+verify as a fallback when no work_email is in-DB. High US/UK coverage (US/UK data is a premium data tier) makes it a good first-call for personal email + mobile in North America/UK; weaker/unproven for EU-resident personal data (GDPR-sensitive) and for pure firmographic/company enrichment (only 40M company profiles), where it should be a fallback behind dedicated B2B providers. For now: hold as a DO-NOT-USE reference row until ADR-0002 constraint is lifted.

### Coresignal
- **Category:** company + employee data-as-a-service (job changes, hiring)
- **Status:** DEPRIORITIZED _(research agent said: EXCLUDED — Coresignal is fundamentally a large-scale public-web data-collection (scraping) business: its flagship employee, company and job-change/hir…; Reclassified per ADR-0009: legitimate DaaS API; public-web provenance is a compliance/ToS assessment (same class as Apollo/ZoomInfo, which are ACTIVE) → not a hard exclusion. Human policy confirmation pending (was EXCLUDED by research agent).)_
- **Capabilities:** job_title, company_domain, employee_count, industry, linkedin_url, funding_stage
- **Summary:** Coresignal is a company + people (employee) data-as-a-service provider specializing in job-change and hiring signals, sold via a credit-based REST/JSON API (Company, Employee, Jobs APIs in Base/Clean/Multi-source variants) plus bulk Datasets. It offers a genuine, well-documented API (apikey-header auth, base URL https://api.coresignal.com/cdapi/v2/), async Bulk Collect, and Employee webhooks for weekly change/job-position-change alerts. Coverage is large (75.7M+ companies, 890.9M+ employees, 460.5M+ job postings). However, the underlying data is collected by scraping publicly available profes…
- **Adversarial verify:** 8 sampled → 8 confirmed, 0 downgraded. All six cited source_urls resolved to genuine, on-topic Coresignal documentation pages; no fabricated or mismatched cit…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API Key sent in the `apikey` HTTP request header (32-char key); keys issued/rotated in the dashboard.coresignal.com self-service platform (API Keys section) or by an account manager. | [link](https://docs.coresignal.com/api-introduction/authorization) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON API. Base URL https://api.coresignal.com/cdapi/v2/ ; e.g. POST /company_base/search/es_dsl with Content-Type: application/json. Separate Company / Employee / Jobs APIs, each in Base, Clean and Multi-source variants. | [link](https://docs.coresignal.com/api-introduction/authorization) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — Bulk Collect endpoints for Company/Employee/Jobs. Asynchronous: POST creates a data request; results retrieved via GET /v2/data_requests/{data_request_id}/files (and /files/{file_name}) or via a completion webhook_url. `limit` param caps records. Docs indicate large per-request result sets (s… | [link](https://docs.coresignal.com/jobs-api/base-jobs-api/endpoints/bulk-collect) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes — Bulk Collect is asynchronous: POST returns a data_request_id (HTTP 201 accepted / 202 in-progress), then poll the /v2/data_requests/{id}/files endpoint or receive a webhook_url callback when the file is ready. | [link](https://docs.coresignal.com/api-introduction/response-codes) _primary_ | 2026-07-01 |
| ✓✓ | pagination | Search returns matching record IDs which are then fetched via Collect. A `limit` parameter caps the number of records returned; Bulk Collect returns the full result set as downloadable files. No explicit cursor/scroll/offset pagination scheme documented on reviewed endpoint pages. | [link](https://docs.coresignal.com/jobs-api/base-jobs-api/endpoints/bulk-collect) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes — Employee/Experience webhooks track profile and job-position (job-change) updates; subscribe by ID list, search-filter query, or Elasticsearch DSL query. Subscription valid 91 days, updates delivered weekly (starting next Monday); webhook events do NOT consume credits; a /v2/subscriptions/simu… | [link](https://docs.coresignal.com/api-introduction/webhooks) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | Per-endpoint per-second limits: Collect (GET) 54 rps; Bulk Collect (POST/GET) 27 rps; Search (POST) 18 rps; Enrich (GET, Multi-source/Clean Company only) 18 rps. Some Employee APIs are per-minute: Base Employee 18/min, Clean Employee 9/min; Real-time Employee accepts 50 URLs/min. Exceeding returns … | [link](https://docs.coresignal.com/api-introduction/rate-limits) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_day | No fixed daily request-count cap documented; throughput is instead bounded by monthly Collect and Search credit allotments per plan/billing cycle (monthly credits reset each cycle; annual plans grant all credits upfront valid 12 months). | [link](https://docs.coresignal.com/introduction/pricing-and-subscriptions) _primary_ | 2026-07-01 |
| ○ | concurrency | No concurrent-request/connection concurrency limit documented (only per-second and per-minute rate limits are published). | — |  |
| ○ | latency_p50_ms | Not published. Synchronous Collect/Search are near-real-time; Real-time Employee API can return a 408 timeout, but no p50 latency figure is documented. | — |  |
| ○ | latency_p95_ms | Not published; no p95 latency figure documented by the provider. | — |  |
| ✓ | coverage | 75.7M+ company records (fully refreshed monthly), 890.9M+ employee records (continuously updated), 460.5M+ job postings (daily additions); ~472M new records added yearly. Global public professional/company web data. | [link](https://coresignal.com/pricing/) _primary_ | 2026-07-01 |
| ○ | accuracy | No published field-level accuracy / match-rate percentage found; provider markets freshness (monthly company refresh, daily jobs) rather than a stated accuracy metric. | — |  |
| ✓ | pricing_model | Credit-based subscriptions with two credit types: Collect credits (1 = one collected/enriched record; 2 for Multi-source Company) and Search credits (1 = one successful query; 2 for Multi-source Company). Tiers: Free ($0, 200 Collect + 400 Search, ~7-14 day trial, no card), Starter from $49/mo, Pro… | [link](https://coresignal.com/pricing/) _primary_ | 2026-07-01 |
| ✓ | price_per_match | Effective per-record price scales with volume: ~$0.133-$0.196/record (Starter), ~$0.05-$0.08/record (Pro), ~$0.005-$0.030/record (Premium). One Collect credit = one record (two for Multi-source Company data). | [link](https://coresignal.com/pricing/) _primary_ | 2026-07-01 |
| ✓ | error_codes | Documented HTTP codes: 200 (success, credits deducted), 201 (Bulk Collect accepted), 202 (Bulk Collect in progress); 400 (bad/missing param), 401 (invalid/missing API key), 402 (insufficient credits), 404 (nonexistent URL/ID), 409 (duplicate Bulk Collect POST), 422 (bad data structure/invalid webho… | [link](https://docs.coresignal.com/api-introduction/response-codes) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | 429 signals per-second rate-limit exceeded (client should throttle and retry). Real-time Employee 408 timeout is explicitly retriable with no credit charge; 454 is not charged. No explicit exponential-backoff / Retry-After header policy is documented. | [link](https://docs.coresignal.com/api-introduction/response-codes) _primary_ | 2026-07-01 |
| ✓ | sdks | No official first-party SDK/client library found. Docs provide copy-paste request examples in cURL, Python, Node.js, Ruby and PHP. A community/third-party Python source exists on dltHub. Integration is direct REST. | [link](https://docs.coresignal.com/api-introduction/authorization) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning; current major version is v2 (base path /cdapi/v2/). A prior v1 existed historically; documentation and examples are on v2. | [link](https://docs.coresignal.com/api-introduction/authorization) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global dataset coverage (company/employee/jobs worldwide). No region-pinned API endpoints, EU/US data-residency options, or hosting-region selection documented. | [link](https://coresignal.com/pricing/) _primary_ | 2026-07-01 |
| ○ | soc2 | No SOC 2 certification advertised; the provider's own data-transparency/security page enumerates ISO 27001 adherence, GDPR/CCPA alignment and EWDCI membership but does NOT mention SOC 2. No SOC 2 report or trust-center attestation found. | — |  |
| ✓ | iso27001 | Self-declared adherence to ISO 27001 requirements (documented Information Security Policy, encryption, access controls, incident response) but explicitly WITHOUT formal ISO 27001 certification, per Coresignal's own statement. | [link](https://coresignal.com/data-transparency/) _primary_ | 2026-07-01 |
| ✓ | gdpr | GDPR-aligned: documented privacy strategy, data processing agreements with vendors, and privacy impact assessments. Collects only publicly available business-related data; excludes login-secured areas and sensitive/personal data (no SSNs, phone numbers, home addresses, biometrics). Note: scraping-o… | [link](https://coresignal.com/data-transparency/) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA-compliant framework (plus Colorado, Utah, Virginia, Connecticut state privacy laws); classifies its data as non-personal publicly-available business information and states it does not register as a data broker. | [link](https://coresignal.com/data-transparency/) _primary_ | 2026-07-01 |

- **Waterfall placement:** Per ADR-0007 reservation-value thinking: DO NOT place in any contact (work_email/personal_email/mobile_phone/direct_dial/office_phone) waterfall — Coresignal explicitly excludes emails and phone numbers, so its marginal contact value is zero. Its only defensible slot is a NON-PRIMARY, legal-approved FALLBACK for (a) job-change / hiring / headcount-trend signals and (b) firmographic refresh (industry, employee_count, funding_stage, linkedin_url), where its 890.9M-employee + 460.5M-job breadth and weekly change-webhooks give real reservation value. Even there it must sit BEHIND compliant, formally-certified providers because of scraping provenance and GDPR right-to-erasure risk; EU-person profiles should be gated/suppressed until a data-provenance review. Given status=EXCLUDED, treat as "bench" — not wired into the live waterfall ordering until compliance sign-off.

### Crunchbase
- **Category:** company + funding data
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** funding_stage, company_domain, employee_count, industry, job_title, linkedin_url
- **Summary:** Crunchbase is a legitimate API-first provider (not a scraper) of institutional-grade private-company, funding, investor, acquisition and IPO data. It exposes a REST/JSON v4 API (base https://api.crunchbase.com/v4/data/) with Entity Lookup, Search (POST), Autocomplete and Deleted Entities endpoints, token auth, keyset pagination, and a documented 200 calls/min rate limit. It is SOC 2 Type II, GDPR and CCPA aligned. Key constraints for our engine: full API requires an Enterprise or Applications license under custom per-end-user pricing (no public self-serve price, no per-match pricing, no free …
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All 8 source_urls resolved to genuine Crunchbase pages; the license-agreement page returned HTTP 500 on first fetch but…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key (token). Passed either as `user_key` URL query parameter or `X-cb-user-key` request header; HTTPS mandatory. | [link](https://data.crunchbase.com/docs/using-the-api) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON. Base URL https://api.crunchbase.com/v4/data/ . Endpoints: Entity Lookup, Search (POST /searches), Autocomplete, Deleted Entities. Plain HTTP rejected with error 426. | [link](https://data.crunchbase.com/docs/using-the-api) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | No dedicated bulk / batch match-by-ID-list endpoint in the REST API. Search API (POST /searches) returns up to 100 entities per page via keyset pagination. Full-database bulk delivery exists only via the separate Data Licensing / Applications program, not the standard REST API. | [link](https://data.crunchbase.com/docs/paginating-through-the-search-api) _primary_ | 2026-07-01 |
| ○ | batch_async | No asynchronous batch/job submission API documented. | — |  |
| ✓✓ | pagination | Keyset (cursor) pagination on the Search API using after_id (uuid of last item) to page forward and before_id (uuid of first item) to page backward; limit up to 100 per request; stable keys guarantee no missed entities as the set changes. | [link](https://data.crunchbase.com/docs/paginating-through-the-search-api) _primary_ | 2026-07-01 |
| ○ | webhooks | No push/webhook mechanism in the v4 API; documented APIs are Lookup, Search, Autocomplete and Deleted Entities (polling) only. | — |  |
| ✓✓ | rate_limit_rps | 200 API calls per minute (~3.3 rps). Exceeding it returns an error; persistent overages require contacting the customer success manager. | [link](https://data.crunchbase.com/docs/using-the-api) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No public per-day call quota; overall volume governed by license tier / number of end users (custom Enterprise terms). | — |  |
| ○ | concurrency | No published concurrency limit. | — |  |
| ○ | latency_p50_ms | Not published by Crunchbase. | — |  |
| ○ | latency_p95_ms | Not published by Crunchbase. | — |  |
| ✓✓ | coverage | 30M+ verified data updates processed annually; 80M+ live professional signals; 4,000+ direct venture partnerships; 1,000+ news outlets and government filings ingested. (Total tracked organization/company count not published on primary pages.) | [link](https://about.crunchbase.com/products/data-licensing) _primary_ | 2026-07-01 |
| ✓✓ | accuracy | Prediction-model accuracy: 84% of funding rounds and 72% of acquisition events correctly predicted; 16K+ predictions validated against real-world outcomes. (Field-level data-accuracy percentage not published; these are predictive-model figures, not enrichment-match accuracy.) | [link](https://about.crunchbase.com/products/crunchbase-api) _primary_ | 2026-07-01 |
| ⚠↓ | pricing_model | Custom annual enterprise licensing. Full API requires an Enterprise or Applications License; priced by number of end users leveraging the data; fees non-refundable, in USD, due at start of term. No public self-serve full-API price; Basic tier exposes only 3 endpoints. Venture Program access is free… | [link](https://data.crunchbase.com/docs/license-agreement) _primary_ | 2026-07-01 |
| ✓ | price_per_match | Not sold per match/per record; licensed by end-user count under custom Enterprise/Applications terms, so no per-match/per-enrichment price exists. | [link](https://data.crunchbase.com/docs/license-agreement) _primary_ | 2026-07-01 |
| ✓ | error_codes | Error 426 returned when HTTPS is not used; requests over the 200/min rate limit return an error response. (Full HTTP status/error-body table not published on the fetched pages.) | [link](https://data.crunchbase.com/docs/using-the-api) _primary_ | 2026-07-01 |
| ○ | retry_behavior | No documented backoff / Retry-After guidance; docs advise contacting the CSM for persistent rate-limit hits. | — |  |
| ○ | sdks | No official Crunchbase-maintained v4 SDK documented; only community wrappers / OpenAPI definitions exist. | — |  |
| ✓ | api_versioning | Versioned in the URL path; current major version v4 (v4.0) served at /v4/data/. Legacy v3.1 and v4.0 (Legacy) also referenced in docs. | [link](https://data.crunchbase.com/docs/using-the-api) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global company dataset; Enterprise access billed in USD with no explicit geographic restriction. No data-residency / regional API hosting options documented. | [link](https://data.crunchbase.com/docs/license-agreement) _primary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 Type II compliant, per Crunchbase Knowledge Center (Data Privacy & Compliance / SOC 2 Compliance articles), covering security, confidentiality, availability and privacy; report available to customers on request. | [link](https://support.crunchbase.com/hc/en-us/articles/19907725612435-SOC-2-Compliance) _primary_ | 2026-07-01 |
| ○ | iso27001 | No ISO/IEC 27001 certification found on primary Crunchbase sources. | — |  |
| ✓ | gdpr | GDPR-aligned program: updated privacy processes for data-subject rights, and a Deleted Entities / delete API endpoint so API licensees can propagate deletions (right to be forgotten). License also obliges licensees to expunge Crunchbase data within 10 days of termination. | [link](https://data.crunchbase.com/docs/deleted-entities) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA-aligned: publishes a California 'Do Not Sell' rights process and person-profile deletion for California residents; discloses that it sells profile data to customers/partners subject to opt-out (privacy@crunchbase.com). | [link](https://support.crunchbase.com/hc/en-us/articles/360041194554-How-Crunchbase-Has-Prepared-for-CCPA-Compliance) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `pricing_model` (no: Partially supported: page confirms Enterprise Access is 'custom pricing based upon number…)

- **Waterfall placement:** Specialist / authoritative source for funding data, NOT a contact-waterfall node (returns no work_email/personal_email/phone). Per ADR-0007 reservation-value thinking: Crunchbase (alongside PitchBook) is one of the few sources of high-value unique funding-round/investor/valuation data, so place it FIRST/authoritative for the funding_stage field and investor/acquisition/IPO enrichment, globally (US + international). For commodity firmographics (company_domain, employee_count, industry) place it as a LATER FALLBACK behind cheaper firmographic providers, since its 200/min rate limit and custom enterprise cost make it uneconomical as a high-throughput primary. Run it as async/batch enrichment (low RPS budget) rather than in the synchronous hot path. Gating: only usable in the customer-facing waterfall under an Applications License; the internal-only Enterprise/Data license prohibits redistribution/display to end customers, so waterfall use is blocked until the correct license tier is contracted.

### Diffbot
- **Category:** company / knowledge-graph enrichment
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** company_domain, employee_count, industry, linkedin_url, job_title, funding_stage
- **Summary:** Diffbot (Diffbot Technologies Corp, US/California) operates the largest Knowledge Graph of the public web (10B+ entities: people, companies, products, articles, discussions; 50+ fields per record), built by its own first-party AI web crawlers with per-entity data provenance. We would consume its clean REST "Enhance" API and asynchronous "Bulk Enhance" API to enrich company/person records. IMPORTANT DISTINCTION vs ADR-0002: although Diffbot itself is a web-crawling/extraction company, it is a LEGITIMATE first-party API provider — it exposes a documented REST + bulk/async API, is NOT a LinkedIn…
- **Adversarial verify:** 8 sampled → 5 confirmed, 3 downgraded. All eight source_urls are genuine, reachable Diffbot pages (docs.diffbot.com and diffbot.com); none appear fabricated o…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Token-based. Single API token passed as `token` query parameter on all GET/POST requests (e.g. ?token=YOURTOKEN). Token obtained from the dashboard. No OAuth; no header auth documented. | [link](https://docs.diffbot.com/reference/authentication) _primary_ | 2026-07-01 |
| ⚠↓ | api_rest | Yes, JSON REST. KG/Enhance base URL https://kg.diffbot.com/kg/v3/ (Enhance endpoint: https://kg.diffbot.com/kg/v3/enhance); Extract/Analyze base URL https://api.diffbot.com/v3/ (e.g. /v3/analyze). GET and POST supported. | [link](https://docs.diffbot.com/reference/introduction-to-enhance-api) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — Bulk Enhance: POST https://kg.diffbot.com/kg/v3/enhance/bulk with a JSON array of input records (type + identifiers like name/url/location). Docs describe processing 'several hundred (or even thousands of) records' per job; exact hard cap not published. | [link](https://docs.diffbot.com/reference/submitbulkjob) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes — Bulk Enhance is asynchronous/job-based: submit returns HTTP 202 with a bulkjobId; job is queued and progress/results/stats polled or downloaded via separate list/download endpoints. | [link](https://docs.diffbot.com/reference/submitbulkjob) _primary_ | 2026-07-01 |
| ○ | pagination | No explicit result-pagination scheme documented for Enhance; single Enhance uses a `size` parameter (default 1) to cap matches returned. Bulk results are retrieved via a download endpoint rather than paged cursors. | — |  |
| ✓✓ | webhooks | Yes (for Bulk Enhance jobs). The bulk submit endpoint accepts an optional `webhookurl` parameter to receive a callback notification on job completion. | [link](https://docs.diffbot.com/reference/submitbulkjob) _primary_ | 2026-07-01 |
| ⚠↓ | rate_limit_rps | Per-plan calls/sec: Free = 5 calls/MINUTE; Startup = 5 calls/sec; Plus = 25 calls/sec; Enterprise = 25+ calls/sec (custom). Exceeding it returns HTTP 429. | [link](https://docs.diffbot.com/reference/rate-limits) _primary_ | 2026-07-01 |
| ⚠↓ | rate_limit_day | No published per-DAY cap. Volume governed by calls/sec + monthly credit allotment: Free 10k, Startup 250k, Plus 1M, Professional 5M credits/mo; a KG entity export/Enhance match = 25 credits (so e.g. Startup ≈ 10k entity enrichments/mo). Free tier also caps Enhance at 400 entities/mo. | [link](https://www.diffbot.com/pricing/) _primary_ | 2026-07-01 |
| ○ | concurrency | No explicit API-call concurrency limit published for Enhance/Extract beyond the calls/sec rate limit. (Crawl/Bulk-Extract jobs have a separate 'active jobs' cap, e.g. 25 active / 1000 total on Plus — not the enrichment API.) | — |  |
| ○ | latency_p50_ms | Not published by Diffbot. | — |  |
| ○ | latency_p95_ms | Not published by Diffbot. | — |  |
| ✓✓ | coverage | Largest public-web Knowledge Graph: 10B+ entities (people, companies, products, articles, discussions); records carry 50+ fields/properties with data provenance; refreshed continuously from ongoing crawls. Notably strong GLOBAL/non-US coverage. | [link](https://www.diffbot.com/products/knowledge-graph/) _primary_ | 2026-07-01 |
| ✓ | accuracy | No published accuracy percentage. Each Enhance match returns a confidence `score`; matching uses a tunable threshold (default 0.29 for single Enhance, 0.32 for Bulk Enhance) to control precision/recall. | [link](https://docs.diffbot.com/docs/day-1-with-knowledge-graph-enhance) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based monthly subscription tiers. Free $0 (10k credits, 5 calls/min), Startup $299/mo (250k credits, $0.001/credit), Plus $899/mo (1M credits, $0.0009/credit), Professional $3,999/mo (5M credits), Enterprise custom. Credits reset monthly (no rollover); overage billed pro-rata. Costs: Extract… | [link](https://www.diffbot.com/pricing/) _primary_ | 2026-07-01 |
| ✓ | price_per_match | One KG entity export / Enhance match = 25 credits. Derived $: Startup $0.001/credit ≈ $0.025 per matched entity; Plus $0.0009/credit ≈ $0.0225 per matched entity (before overage/Enterprise discounts). | [link](https://www.diffbot.com/pricing/) _primary_ | 2026-07-01 |
| ✓ | error_codes | Standard HTTP status codes. 429 = 'Too many requests' when calls/sec rate limit exceeded; 429 'Quota Exceeded' when monthly credit allotment (e.g. Free 10k) is reached. Bulk submit returns 202 on acceptance. | [link](https://docs.diffbot.com/reference/error-429-too-many-requests) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | On HTTP 429 rate-limit error, documented guidance is to stop calls for 1 second and then retry (client-side backoff); credit-quota 429s reset at the start of the billing period. No server Retry-After header documented. | [link](https://docs.diffbot.com/reference/error-429-too-many-requests) _primary_ | 2026-07-01 |
| ✓ | sdks | Official Python client library + CLI: github.com/diffbot/diffbot-python (and legacy diffbot-python-client). Official no-code connectors: Google Sheets add-on (=ENHANCE_ORGANIZATION functions), Excel Add-in. Community clients exist for Go and Rust. | [link](https://github.com/diffbot/diffbot-python) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning at v3. KG/Enhance under /kg/v3/ (kg.diffbot.com); Extract under /v3/ (api.diffbot.com). Current major version is v3. | [link](https://docs.diffbot.com/reference/authentication) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global SaaS, US-hosted (Diffbot Technologies Corp, California). No documented per-region API endpoints or EU data-residency option; cross-border transfers of EU/UK personal data are covered by EU Standard Contractual/Model Clauses per the DPA. | [link](https://docs.diffbot.com/docs/is-diffbot-compliant-with-gdpr) _primary_ | 2026-07-01 |
| ○ | soc2 | Not confirmed on any primary Diffbot source. Only an unreliable third-party (SourceForge) listing asserts SOC 2 — insufficient to verify; no Diffbot trust-center/SOC 2 report located. | — |  |
| ○ | iso27001 | Not confirmed on any primary Diffbot source. Only an unreliable third-party listing asserts ISO 27001 — insufficient to verify. | — |  |
| ✓ | gdpr | Compliant. States adherence to EU/UK GDPR; offers a DPA on request; uses EU Model Clauses for transfers outside the EEA; supports data-subject rights (Articles 12-23) incl. right to erasure — deleted personal data is placed on a suppression list; access/export in CSV/JSON; breach notification. EEA/… | [link](https://docs.diffbot.com/docs/is-diffbot-compliant-with-gdpr) _primary_ | 2026-07-01 |
| ✓ | ccpa | Compliant. Maintains a CCPA/CPRA privacy statement for California residents (incorporated into the Privacy Policy since July 2020); honors verifiable deletion requests and opt-out; privacy contact privacy@diffbot.com. | [link](https://docs.diffbot.com/docs/what-is-diffbots-ccpa-policy) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `api_rest` (no: Cited page confirms JSON REST, KG base https://kg.diffbot.com/kg/v3/, the /enhance endpoi…); `rate_limit_rps` (no: Free (5 Calls Per Minute), Startup (5 Calls Per Second) and Plus (25 Calls Per Second) ma…); `rate_limit_day` (no: Confirmed: Free 10,000 / Startup 250,000 / Plus 1,000,000 credits, no per-day cap, and 25…)

- **Waterfall placement:** Place Diffbot in the COMPANY/FIRMOGRAPHIC + ENTITY-RESOLUTION lane, NOT the work_email/mobile_phone contact waterfall. Recommended ordering (ADR-0007 reservation-value): (1) PRIMARY / early for global company enrichment fields — company_domain, employee_count, industry, funding_stage — and for org/person LinkedIn-URL and social-profile resolution, especially for NON-US / international records where US-centric B2B databases (ZoomInfo/Apollo/Clearbit) have thin coverage; Diffbot's 10B+ global Knowledge Graph is its highest reservation value there. (2) PARALLEL/fallback firmographic filler behind higher-confidence firmographic providers for US mid-market. (3) DO NOT use as a source in the verified-email or phone waterfall — it returns no email_status/phone_status and any emails/phones are opportunistic public-web values, not validated contact data. Cost-wise (25 credits ~ $0.0225-$0.025/match) it is competitive as a coverage-filler; reserve credit spend for the hard-to-fill international company/entity records rather than routine US firmographics. Consume via async Bulk Enhance (POST /kg/v3/enhance/bulk with webhookurl) for batch backfills and the sync Enhance endpoint for real-time single lookups.</parameter> <parameter name="sources">["https://docs.diffbot.com/reference/authentication", "https://docs.diffbot.com/reference/introduction-to-diffbot-apis", "https://docs.diffbot.com/reference/introduction-to-enhance-api", "https://docs.diffbot.com/docs/day-1-with-knowledge-graph-enhance", "https://docs.diffbot.com/reference/submitbulkjob", "https://docs.diffbot.com/reference/rate-limits", "https://docs.diffbot.com/reference/error-429-too-many-requests", "https://www.diffbot.com/pricing/", "https://docs.diffbot.com/docs/how-credits-work", "https://www.diffbot.com/products/knowledge-graph/", "https://docs.diffbot.com/docs/is-diffbot-compliant-with-gdpr", "https://docs.diffbot.com/docs/what-is-diffbots-ccpa-policy", "https://github.com/diffbot/diffbot-python"]

### The Companies API
- **Category:** company / firmographics
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** company_domain, employee_count, industry, technographics, linkedin_url
- **Summary:** The Companies API (thecompaniesapi.com; SAS registered in Nantes, France) is a legitimate REST API-first company firmographics enrichment provider covering 50M+ companies with 300+ data points. It enriches a company from a domain, email, or social-network URL and offers company search, similar-company lookup, lists, and webhooks. Data is generated on-demand by AI models that analyze the company's own website, social networks, search engines, and other public sources — this is COMPANY (firmographic) data, not people/contact/LinkedIn-profile scraping, so it does not trip the ADR-0002 scraping/T…
- **Adversarial verify:** 8 sampled → 6 confirmed, 2 downgraded. All 8 source URLs resolved and are authentic (official thecompaniesapi.com/api/* docs pages plus the genuine thecompani…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API token via HTTP header 'Authorization: Basic MY-API-TOKEN' (also accepted as '?token=' query param). Tokens are permanent and never expire. | [link](https://www.thecompaniesapi.com/api/authentication) _primary_ | 2026-07-01 |
| ⚠↓ | api_rest | RESTful JSON API. Base URL https://api.thecompaniesapi.com/v2 (e.g. GET /v2/companies/:domain). Form-encoded request bodies, JSON responses. | [link](https://www.thecompaniesapi.com/api/authentication) _primary_ | 2026-07-01 |
| ⚠↓ | bulk_api | Bulk enrichment supported via asynchronous 'actions' jobs: request_action(job='enrich-companies', domains=[...]) queues a batch, results polled via fetch_actions. Max batch size for enrichment not explicitly documented; the list toggle endpoint accepts up to 1,000 domains per request (secondary). | [link](https://raw.githubusercontent.com/thecompaniesapi/sdk-ruby/main/README.md) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes. Asynchronous job/action pattern: submit a job (e.g. 'enrich-companies') via request_action, then poll status/results via fetch_actions (status, page, size). Docs advertise both synchronous and asynchronous enrichment options. | [link](https://raw.githubusercontent.com/thecompaniesapi/sdk-ruby/main/README.md) _primary_ | 2026-07-01 |
| ✓✓ | pagination | Page-based pagination via 'page' and 'size' parameters (used on search and fetch_actions endpoints). | [link](https://raw.githubusercontent.com/thecompaniesapi/sdk-ruby/main/README.md) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes. Configurable webhooks push a payload to a user-defined URL on events such as an enrichment/operation completing or a new company being added to a list; managed in account settings. | [link](https://www.thecompaniesapi.com/api/webhooks) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | Plan-tiered requests/second: Startup 50 rps, Scaleup 250 rps, Enterprise 1,000 rps. Exceeding returns HTTP 429 Too Many Requests. | [link](https://www.thecompaniesapi.com/api/rate-limits) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No per-day request rate limit published; usage is governed by a monthly credit quota (Startup 50,000 / Scaleup 250,000 / Enterprise 500,000 credits per month), not a documented daily API cap. | — |  |
| ○ | concurrency | No documented concurrency limit (only per-second rate limits are published). | — |  |
| ○ | latency_p50_ms | Not published by provider. | — |  |
| ○ | latency_p95_ms | Not published by provider. | — |  |
| ✓✓ | coverage | 50M+ companies in database, 300+ data points per company; on-demand enrichment can analyze essentially any domain not already cached (~300K companies scanned per 24h). Firmographic scope only (no people/contact data). | [link](https://www.thecompaniesapi.com/) _primary_ | 2026-07-01 |
| ○ | accuracy | No quantified accuracy/match-rate metric published; provider states the schema is refined over time and offers 'refresh' to re-analyze for up-to-date data. | — |  |
| ✓ | pricing_model | Credit-based monthly subscription. Startup $95/mo (50,000 credits), Scaleup $295/mo (250,000 credits), Enterprise $595/mo (500,000 credits). Overage credits: $2.50 / $1.75 / $1.50 per 1,000 by tier. Annual billing gives ~2 months free. No free tier. | [link](https://www.thecompaniesapi.com/pricing) _primary_ | 2026-07-01 |
| ✓ | price_per_match | 1 credit per company enrichment (a 'refresh'/re-analyze enrichment costs 11 credits total; a 'simplified' profile is free). Effective ~$0.0012–$0.0025 per enrichment depending on plan credit rate. | [link](https://www.thecompaniesapi.com/api/enrich-company-from-domain) _primary_ | 2026-07-01 |
| ✓ | error_codes | Standard HTTP codes: 200 OK; 400 bad request; 401 no valid token; 402 valid params but request failed; 403 insufficient permission; 404 not found; 409 conflict (idempotency key); 429 too many requests; 500/502/503/504 server errors. Error type strings: apiConnectionError, apiError, authenticationEr… | [link](https://www.thecompaniesapi.com/api/errors) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | On 429, provider recommends exponential backoff and retry after a short delay. Idempotency keys supported (reuse returns 409 conflict to avoid duplicate processing). | [link](https://www.thecompaniesapi.com/api/errors) _primary_ | 2026-07-01 |
| ✓ | sdks | Official SDKs: Python, TypeScript/Node, Ruby, PHP, Go (all MIT-licensed on GitHub), plus an n8n integration node package. | [link](https://github.com/thecompaniesapi) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based versioning; current version is v2 (all endpoints under /v2/...). Prior v1 existed. | [link](https://www.thecompaniesapi.com/api/enrich-company-from-domain) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Single EU/France-based operation (SAS registered in Nantes, France). Privacy policy states data is processed at the company's operating offices and may be transferred/maintained outside a user's jurisdiction; no documented multi-region hosting or data-residency options. | [link](https://www.thecompaniesapi.com/product/privacy) _primary_ | 2026-07-01 |
| ○ | soc2 | No SOC 2 certification or trust/security page found on provider site. | — |  |
| ○ | iso27001 | No ISO 27001 certification found on provider site. | — |  |
| ○ | gdpr | Provider is EU/France-based and therefore subject to GDPR, but the published privacy policy does not make an explicit GDPR-compliance statement or describe data-subject access/deletion mechanisms or a DPA; no explicit compliance claim to cite. | — |  |
| ○ | ccpa | No CCPA / California privacy rights statement found in the provider's privacy policy. | — |  |

**Downgraded on re-check:** `api_rest` (no: Core confirmed: base URL /v2, example GET /v2/companies/thecompaniesapi.com, JSON respons…); `bulk_api` (no: Primary claim verified: request_action(domains, job:'enrich-companies', estimate:false) +…)

- **Waterfall placement:** Company/firmographics waterfall only (never for person emails/phones — it returns no people/contact data). Fields it can fill: company_domain (canonicalization), industry, employee_count/size band, technographics, company location/country, founded year, revenue estimate, and company social/LinkedIn URL. Recommended placement (ADR-0007 reservation-value logic): use as an EARLY parallel or first-tier firmographics call for its very low marginal cost (~$0.0012–$0.0025/enrichment = 1 credit) and its on-demand coverage advantage — it can enrich long-tail and EU/global domains that cached premium DBs miss, and can re-analyze on demand (refresh) for freshness. Its high reservation value (cheap + broad coverage) argues for calling it before expensive premium firmographics providers for low-stakes fill and for EU/international company coverage. However, because accuracy/match-rate, latency, and enterprise compliance (SOC 2 / ISO / explicit GDPR-CCPA DPA) are UNVERIFIED and there is no SLA, do NOT make it the sole primary for high-stakes firmographic fields — keep a premium provider ahead of or alongside it for authoritative employee-count/revenue, and treat The Companies API as the fallback/broad-coverage tier and as the preferred first call for domain->firmographics on European and long-tail domains. Integrate synchronously for single-record real-time paths and via the async 'actions' bulk-job pattern for batch backfills.

### BuiltWith
- **Category:** technographics
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** technographics, company_domain, employee_count, office_phone, work_email, linkedin_url
- **Summary:** BuiltWith is a legitimate, API-first technographics provider that detects a website's technology stack (and attaches firmographic metadata: traffic/rank scores, estimated tech spend & eCommerce revenue, company name/address, generic company phone/email, social profiles, employee counts) by crawling public web pages, DNS, tag managers, ads.txt, etc. It is NOT a LinkedIn/contact scraper and does not resell person-level PII. It exposes a mature REST/HTTPS API surface: Domain API (v22, up to 16 domains/call), Bulk Domain API, Lists API (offset pagination), Relationships, Trends, Change, Company-t…
- **Adversarial verify:** 8 sampled → 8 confirmed, 0 downgraded. No fabricated-looking citations. All four source_urls resolved to genuine BuiltWith documentation/KB pages containing t…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key. Two methods: query parameter ?KEY=<GUID> or HTTP header 'Authorization: API <GUID>'. HTTPS-only; docs warn 'Never expose your API key.' | [link](https://api.builtwith.com/domain-api) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | Yes - REST over HTTPS. Domain API base URL https://api.builtwith.com/v22/api.{json\|xml\|csv}. Many sibling endpoints under api.builtwith.com (Lists, Relationships, Trends, Change, Trust, Company-to-URL, etc.). | [link](https://api.builtwith.com/domain-api) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes. Domain API accepts up to 16 domains per call as CSV in the LOOKUP parameter. A separate Bulk Domain API supports larger uploaded or API-passed domain lists (its exact max batch size is not published). | [link](https://api.builtwith.com/domain-api) _primary_ | 2026-07-01 |
| ○ | batch_async | Bulk Domain API is described as processing a list without waiting on single-domain calls one at a time, but no explicit asynchronous job-submit/poll contract (job id, status endpoint) is documented on primary docs. | — |  |
| ✓✓ | pagination | Offset-based (Lists API): pass OFFSET set to the NextOffset value returned by the prior response; NextOffset='END' indicates no more results. | [link](https://api.builtwith.com/lists-api) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | No traditional HTTP callback webhooks. Real-time push is offered via the Live Feed API over WebSocket (wss://sync.builtwith.com/wss/). | [link](https://api.builtwith.com/) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | 10 requests/second (response header X-RATELIMIT-LIMIT-PERSECOND: 10). Exceeding returns HTTP 429. | [link](https://api.builtwith.com/domain-api) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No published daily request cap; throughput is governed by API-credit balance rather than a per-day rate limit. | — |  |
| ✓✓ | concurrency | Max 8 concurrent requests (response header X-RATELIMIT-LIMIT-CONCURRENT: 8). Exceeding returns HTTP 429. | [link](https://api.builtwith.com/domain-api) _primary_ | 2026-07-01 |
| ○ | latency_p50_ms | Not published by BuiltWith. | — |  |
| ○ | latency_p95_ms | Not published by BuiltWith. | — |  |
| ○ | coverage | Tracks tens of thousands of web technologies across a large share of the public web (Domain API indexes internal pages, subdomains, tag managers, ads.txt, versions), but no exact coverage counts (technologies tracked / sites indexed) are stated on primary API docs. | — |  |
| ○ | accuracy | No published detection accuracy / precision-recall figures. | — |  |
| ✓✓ | pricing_model | Subscription + API-credit model. Basic $295/mo (2 technologies, 2,000 domains/mo). Pro $495/mo (unlimited technologies, 20,000 upload-analysis credits). Team $950/mo or $9,950/yr (unlimited except API credits: 100,000/mo or 1.2M/yr, multi-user). Advanced $144/yr for individual detailed site lookups… | [link](https://kb.builtwith.com/general-questions/plans-and-pricing-explained/) _primary_ | 2026-07-01 |
| ○ | price_per_match | Not published as a per-match USD unit price; consumption is credit-based (Team plan includes 100,000 API credits/mo, 1.2M/yr; different endpoints/options consume different credit amounts). A blended ~$0.01/lookup can be inferred on Team but is not officially stated. | — |  |
| ✓ | error_codes | Documented numeric codes: -1 unsupported return type; -2 API key wrong (must be a GUID); -3 out of API credits; -4 technology name not found; -5 max technologies reached (upgrade needed); -6 error saving technology usage; -7 only one lookup at a time for this endpoint; -8 invalid/unsupported root d… | [link](https://api.builtwith.com/errorCodes) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | HTTP 429 returned when per-second or concurrency limits are exceeded; rate-limit headers (X-RATELIMIT-LIMIT-PERSECOND / X-RATELIMIT-LIMIT-CONCURRENT) are provided so clients can self-throttle. No documented server-side automatic retry/backoff policy - client is expected to back off. | [link](https://api.builtwith.com/domain-api) _primary_ | 2026-07-01 |
| ✓ | sdks | Official BuiltWith CLI and TUI for terminal access, plus an official MCP API/server exposing endpoints as LLM tools. No official multi-language client SDKs; a community TypeScript library (zcaceres/builtwith-api) exists as a third party. | [link](https://api.builtwith.com/) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path-based, per-endpoint version tokens: Domain API v22, Relationships rv4, Lists lists12, Trends v6, Vector Search v1, Trust v1 (each pinned in the URL path). | [link](https://api.builtwith.com/) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global API access. Data hosted in Canada (primary data center) with an Australian mirror data center. | [link](https://builtwith.com/privacy) _primary_ | 2026-07-01 |
| ✓ | soc2 | Hosting data centers are SOC 1 Type II and SOC 2 Type II certified (Canada primary and Australian mirror). This is data-center/infrastructure-level certification per the privacy policy; a separate organization-wide BuiltWith SOC 2 report is not independently evidenced. | [link](https://builtwith.com/privacy) _primary_ | 2026-07-01 |
| ✓ | iso27001 | Hosting data centers are ISO/IEC 27001 certified (Canada primary; Australian mirror also lists ISO 14001, 22301, 27001, PCI DSS). Data-center-level certification per the privacy policy. | [link](https://builtwith.com/privacy) _primary_ | 2026-07-01 |
| ✓ | gdpr | GDPR-compliant. BuiltWith Pty Ltd acts as data controller; documents legal bases (contractual necessity, legitimate interests, consent, legal obligations) and data-subject rights (access, correction, deletion, restrict/object, portability). Since 2018-05-01 it no longer provides or stores EU person… | [link](https://builtwith.com/privacy) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA rights honored: California residents may request disclosure of the categories and specific pieces of personal information collected, and may request deletion of such information, subject to applicable legal exceptions under the CCPA. | [link](https://builtwith.com/privacy) _primary_ | 2026-07-01 |

- **Waterfall placement:** PRIMARY technographics provider, enriched by company_domain, with global coverage (US + EU + APAC). Per ADR-0007 reservation-value thinking, reserve BuiltWith credits for the two fields where its data is uniquely high-value: (a) the technographics field on a known domain (Domain API v22, batch up to 16 domains/call), and (b) technology-install-based company discovery / TAL building via the Lists API. Do NOT spend it on person-level waterfalls: its work_email and office_phone are company-generic and its linkedin_url is a company social handle, so for those fields place BuiltWith only as a last-resort fallback behind dedicated contact providers (never first or parallel). It carries no person-level PII, so it does not compete in the work_email / personal_email / mobile_phone / direct_dial waterfalls. Operational placement: enrich by domain, self-throttle to <=10 rps and <=8 concurrent, honor 429 backoff via rate-limit headers.

### HG Insights
- **Category:** technographics + IT intent
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** company_domain, employee_count, industry, technographics, intent_topics
- **Summary:** HG Insights is a legitimate API-first, company-level enrichment provider specializing in technographics, IT spend estimates, AI-maturity, and IT/buyer intent (HG Contextual Intent + TrustRadius signals). It exposes a modern v2 REST/JSON API (base https://api.hginsights.com/data-api/v2) with Bearer-token API-key auth, synchronous batch enrichment up to 25 companies/request, and a documented 25 rps rate limit. It is a data-vendor/enrichment API, not a LinkedIn scraper — the model is credits-based and API-served, so it passes the ADR-0002 API-first/no-scraping gate. It is a COMPANY/account enric…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All four cited URLs are genuine, reachable HG Insights docs pages, and load-bearing details (verbatim base URL, endpoin…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key as HTTP Bearer token in the Authorization header (Authorization: Bearer hg_v2_...). Keys are self-served from admin.hginsights.com (Settings > API Keys) and require an active HG Platform subscription with credits. | [link](https://data-docs.hginsights.com/v2/guides/authentication) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | Yes — REST/JSON API v2. Base URL https://api.hginsights.com/data-api/v2. Endpoints incl. POST /companies/enrich (firmographics+technographics+IT spend+AI maturity+contracts), GET /companies/functional-areas, GET /companies/hierarchy, POST /intent/enrich, GET /contracts/search. Accepts HG ID or doma… | [link](https://data-docs.hginsights.com/v2/guides/overview) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — synchronous batch enrichment via /companies/enrich accepts up to 25 companies per request and counts as a single request regardless of batch size. Max batch = 25. | [link](https://data-docs.hginsights.com/v2/guides/rate-limiting) _primary_ | 2026-07-01 |
| ○ | batch_async | No async/job-based batch endpoint documented; batch enrichment is synchronous (up to 25 companies/request). | — |  |
| ○ | pagination | Pagination scheme for list/search endpoints (e.g. /contracts/search) not documented in fetched pages. | — |  |
| ○ | webhooks | No webhooks/push callbacks documented; API is request/response only. | — |  |
| ✓✓ | rate_limit_rps | 25 requests per second across all v2 API endpoints. | [link](https://data-docs.hginsights.com/v2/guides/rate-limiting) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No fixed per-day request cap published; usage is governed by a credits model (each returned data point consumes credits), not a daily request quota. | — |  |
| ○ | concurrency | No explicit concurrent-connection/concurrency limit documented (only the 25 rps limit). | — |  |
| ○ | latency_p50_ms | Not published. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ✓✓ | coverage | Global company coverage — API returns firmographics, technographics (products/vendors/usage signals), IT spend estimates by category, AI-maturity score, contracts, and intent (HG Contextual Intent + TrustRadius) for effectively any company by domain or HG ID. Specific coverage counts (companies/tec… | [link](https://data-docs.hginsights.com/v2/guides/overview) _primary_ | 2026-07-01 |
| ○ | accuracy | No published match/accuracy or precision/recall metrics. | — |  |
| ✓✓ | pricing_model | Credits-based annual subscription with tiered plans (Starter, Growth, Enterprise) organized by seats licensed, credits consumed, and leads processed. Two credit types: Intelligence credits (data export) and AI action credits (platform actions). Modules (Market Analyzer, Data Studio, Sales Copilot) … | [link](https://hginsights.com/product/pricing-guide/) _primary_ | 2026-07-01 |
| ✓✓ | price_per_match | Priced in credits per data point, not USD per match: e.g. a firmographics-and-hierarchy entity costs 0.1 credit while a buyer-intent data point costs 3 credits; credit unit price decreases as plan scales. No public USD price per match/record. | [link](https://hginsights.com/product/pricing-guide/) _primary_ | 2026-07-01 |
| ⚠↓ | error_codes | Standard HTTP status codes: 429 Too Many Requests on rate-limit breach (with x-ratelimit-reset header = seconds until window resets); 401 Unauthorized when API key is missing/incorrect/revoked; 422 Unprocessable Entity when required body params (companies, fields) are invalid/missing. | [link](https://data-docs.hginsights.com/v2/guides/rate-limiting) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | On 429, wait x-ratelimit-reset seconds before retrying; guidance is to spread requests evenly across the 1-second window and avoid bursts. No documented server-side automatic retry. | [link](https://data-docs.hginsights.com/v2/guides/rate-limiting) _primary_ | 2026-07-01 |
| ○ | sdks | No official client SDKs/libraries found; documentation shows raw REST/HTTP usage only. | — |  |
| ✓ | api_versioning | Path/major-version based: current API v2 (path /v2, base https://api.hginsights.com/data-api/v2); legacy v1 deprecated with sunset date September 1, 2026. Migration v1->v2 described as straightforward. | [link](https://data-docs.hginsights.com/) _primary_ | 2026-07-01 |
| ○ | regional_availability | US-based vendor with global company data coverage; no multi-region API hosting/data-residency options documented. EU/UK/Swiss personal-data transfer covered via Data Privacy Framework (see gdpr). | — |  |
| ✓ | soc2 | SOC 2 Type II — HG Insights states it has successfully completed SOC 2 Type II certification (covering security, availability, and confidentiality for products including HG Platform, HG Universe, and DaaS). | [link](https://hginsights.com/privacy-page/) _primary_ | 2026-07-01 |
| ○ | iso27001 | No ISO 27001 certification referenced on HG Insights security/privacy pages (only SOC 2 Type II is cited). | — |  |
| ✓ | gdpr | Addressed via cross-border transfer frameworks: HG Insights, Inc. certifies compliance with the EU-U.S. Data Privacy Framework (EU-U.S. DPF), the UK Extension to the EU-U.S. DPF, and the Swiss-U.S. DPF. Data subjects have rights to access, correct, restrict, and delete personal data consistent with… | [link](https://hginsights.com/privacy-page/) _primary_ | 2026-07-01 |
| ✓ | ccpa | Addressed: dedicated California (CCPA/CPRA) privacy-notice section; HG states 'We do not sell Personal Data as the term sell is commonly understood' while noting some Service Provider activity 'may be deemed a sale under CCPA/CPRA'; opt-out/rights via legal@hginsights.com. | [link](https://hginsights.com/privacy-page/) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `error_codes` (no: Only the 429 portion is supported by the cited page ('429 Too Many Requests'; check x-rat…)

- **Waterfall placement:** first/primary for technographics + intent_topics (and IT-spend/AI-maturity) on enterprise & global accounts; parallel/secondary for firmographic fields (company_domain, employee_count, industry) behind cheaper generalists; NOT placed in any work_email/personal_email/mobile_phone/direct_dial waterfall since HG returns no person-level contact fields. Region: global coverage, US-hosted, EU/UK/Swiss transfers via DPF. Credits-based cost (0.1 credit firmographic vs 3 credits intent) argues for ordering it after free/cheap sources on shared fields — ADR-0004/0007/0008.

### Datanyze
- **Category:** technographics
- **Status:** EXCLUDED _(research agent said: EXCLUDED — Datanyze has been a ZoomInfo-owned product since the 2018 acquisition and no longer offers a usable, API-first enrichment surface for our e…; Hard-excluded: ZoomInfo-absorbed, no viable standalone API (defunct/continuity). ADR-0009.)_
- **Capabilities:** technographics, company_domain, industry, employee_count, work_email, direct_dial, mobile_phone, job_title, linkedin_url
- **Summary:** Datanyze (technographics origin, ZoomInfo-owned since 2018) is EXCLUDED. Its legacy technographics REST API is deprecated — the official API docs 301-redirect to the homepage and no current API reference exists. The live product is a Chrome-extension contact tool that reveals emails/direct-dials over LinkedIn and company pages; no plan exposes an API and its terms bar automated access without written approval, making it incompatible with our API-first, no-scraping engine (ADR-0002). Coverage (~120M contacts / ~84M emails / ~48M direct dials) is verified from the official Chrome Web Store list…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. All four unique source URLs resolved and returned real content (Chrome Web Store, Prospeo comparison, LinkAPI docs, Zoo…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Legacy/deprecated technographics API used a bearer token passed as a query-string parameter over HTTPS. No current public API or documented auth scheme (developer docs have been retired). | [link](https://developers.linkapi.solutions/docs/datanyze) _secondary_ | 2026-07-01 |
| ✓✓ | api_rest | Legacy REST API base URL https://api.datanyze.com (HTTPS; GET endpoints /industries, /technology_types, /technologies, /customers, /countdown, /domain_info, /domain_history, /revenue; POST /add_domain). DEPRECATED: official API docs section (support.datanyze.com/hc/en-us/sections/115001612906-API) … | [link](https://developers.linkapi.solutions/docs/datanyze) _secondary_ | 2026-07-01 |
| ○ | bulk_api | No documented bulk/batch API. The Chrome extension offers UI-level 'bulk employee retrieval' from company pages, but no programmatic batch endpoint or max batch size is published. | — |  |
| ○ | batch_async | No async/batch job API documented. | — |  |
| ○ | pagination | No pagination scheme documented for the (deprecated) API. | — |  |
| ○ | webhooks | No webhook/event delivery documented. Legacy product had domain-tracking 'alerts' in-app, but no programmatic webhook interface is published. | — |  |
| ○ | rate_limit_rps | No per-second rate limit published. | — |  |
| ○ | rate_limit_day | No daily rate limit published. Legacy secondary mentions of a ~100-calls/month quota for paying customers are stale and pertain to the retired API; not verifiable from a primary source. | — |  |
| ○ | concurrency | No concurrency limits published. | — |  |
| ○ | latency_p50_ms | Not published (API deprecated; no SLA). | — |  |
| ○ | latency_p95_ms | Not published (API deprecated; no SLA). | — |  |
| ✓✓ | coverage | Contact database advertised at ~120M contacts, ~84M email addresses, and ~48M direct-dial numbers (official Chrome Web Store listing). Technographic domain coverage is no longer separately published under the Datanyze brand. | [link](https://chromewebstore.google.com/detail/datanyze-chrome-extension/mlholfadgbpidekmhdibonbjhdmpmafd) _primary_ | 2026-07-01 |
| ○ | accuracy | No published accuracy/match-rate guarantee. Third-party review notes ~15-20% estimated data waste on reveals, but no vendor-primary accuracy figure exists. | — |  |
| ✓✓ | pricing_model | Credit-based self-serve subscription (no API/usage tier). Nyze Lite: free 90-day trial, 10 credits/mo. Nyze Pro 1: $29/mo ($21/mo billed annually) = 80 credits/mo. Nyze Pro 2: $55/mo ($39/mo billed annually) = 160 credits/mo. No API access included in any plan; no credit rollover. | [link](https://prospeo.io/s/datanyze-pricing) _secondary_ | 2026-07-01 |
| ✓✓ | price_per_match | 1 credit = 1 contact reveal (email + phone for a single person); credits are consumed on click regardless of data quality returned. List per-credit cost ~$0.24-$0.36; effective ~$0.29-$0.45 per usable contact after estimated data waste. | [link](https://prospeo.io/s/datanyze-pricing) _secondary_ | 2026-07-01 |
| ○ | error_codes | No error-code catalog published (API deprecated). | — |  |
| ○ | retry_behavior | No documented retry/backoff guidance. | — |  |
| ○ | sdks | No official SDKs documented. Third-party API directory lists an 'SDKs' section but provides no actual client libraries or details. | — |  |
| ○ | api_versioning | No API versioning scheme published (legacy API undocumented; current product has no API). | — |  |
| ○ | regional_availability | No API regional endpoints/data-residency options published. Vendor is US-based (ZoomInfo) with global data; no region controls documented. | — |  |
| ✓✓ | soc2 | Parent company ZoomInfo holds a SOC 2 Type II attestation (audited by SC&H Attest Services). This is a ZoomInfo-org certification, not a Datanyze-product-specific report. | [link](https://ir.zoominfo.com/news-releases/news-release-details/zoominfo-continues-strengthen-its-commitment-security-and/) _primary_ | 2026-07-01 |
| ✓✓ | iso27001 | Parent ZoomInfo is ISO 27001 certified for its information security management system (ISMS). Certification is at the ZoomInfo org level, not Datanyze-specific. | [link](https://ir.zoominfo.com/news-releases/news-release-details/zoominfo-continues-strengthen-its-commitment-security-and/) _primary_ | 2026-07-01 |
| ⚠↓ | gdpr | Parent ZoomInfo maintains a GDPR compliance program (data-subject rights honored across its database; TRUSTe GDPR practices validation). Referenced as a compliance commitment/program, not a formal certification; applies at ZoomInfo level. | [link](https://ir.zoominfo.com/news-releases/news-release-details/zoominfo-continues-strengthen-its-commitment-security-and/) _primary_ | 2026-07-01 |
| ✓ | ccpa | Parent ZoomInfo maintains a CCPA compliance program (TRUSTe CCPA practices validation; self-service privacy center). Compliance commitment at the ZoomInfo level, not a Datanyze-product certification. | [link](https://ir.zoominfo.com/news-releases/news-release-details/zoominfo-continues-strengthen-its-commitment-security-and/) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `gdpr` (no: The source emphasizes general 'GDPR and CCPA compliance' (plus Privacy Shield certificati…)

- **Waterfall placement:** EXCLUDED — do not place Datanyze in the waterfall. Even though its category is technographics (its historical strength), it offers no consumable API-first surface today: the legacy technographics REST API is deprecated (docs 301-redirect to homepage) and the live product is a Chrome-extension contact tool whose terms bar automated access. Under ADR-0007 reservation-value thinking its programmatic reservation value is effectively zero (unreachable via server-to-server API, and browser-overlay delivery conflicts with ADR-0002 no-scraping). Recommended substitute: for the technographics field/stage, source technographic signals from ZoomInfo's API (the parent that absorbed Datanyze's dataset) as a first/parallel technographics provider for all regions; keep Datanyze out of every waterfall stage. Do not use Datanyze for work_email/direct_dial/mobile_phone either, as those are only exposed via the non-API extension.

### PredictLeads
- **Category:** signals: job postings, technology changes, company/news
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** company_domain, employee_count, industry, technographics, intent_topics, funding_stage
- **Summary:** PredictLeads is a legitimate API-first company-intelligence and B2B signals provider (NOT a personal-data/LinkedIn scraper), covering 120M+ companies worldwide with six datasets: Job Openings, Technologies (technographics), News Events (incl. funding/M&A), Companies (firmographics), Similar Companies, and Key Customers/Connections. Data is delivered via a REST/JSON API (base https://predictleads.com/api/v3/), real-time webhooks, full flat-file exports (S3/GCS/SFTP), and an MCP server. Collection is from publicly available company web sources (company websites, job boards, press releases) rath…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. No fabricated citations detected; all eight source URLs resolve to genuine PredictLeads properties (docs.predictleads.c…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key authentication: X-Api-Key and X-Api-Token request headers (also accepted as api_key & api_token query-string parameters). No OAuth. | [link](https://docs.predictleads.com/v3/api_endpoints/introduction/authentication) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | REST/JSON API. Base URL https://predictleads.com/api/v3/. Example endpoints: /companies/{domain}/job_openings, /companies/{domain}/technology_detections, /companies/{domain}/news_events, /discover/technologies/{id}/technology_detections. | [link](https://blog.predictleads.com/2024/08/21/introducing-predictleads-new-technology-detection-api-endpoint) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | No synchronous bulk/batch match API endpoint documented (enrichment is per-company lookup, returning up to 1,000 records per request). Bulk delivery is via Enterprise full flat-file exports over AWS S3, GCS, or SFTP. Max batch size not published. | [link](https://predictleads.com/pricing) _primary_ | 2026-07-01 |
| ○ | batch_async | No asynchronous batch-job API documented; flat-file exports (S3/GCS/SFTP) serve asynchronous bulk delivery needs. | — |  |
| ✓✓ | pagination | Page-based pagination via the `page` query parameter; pagination metadata (e.g. `count`) is included in responses only when `page` is supplied (for performance). | [link](https://docs.predictleads.com/) _primary_ | 2026-07-01 |
| ✓✓ | webhooks | Yes. Real-time webhooks supported across datasets (Companies, Job Openings, Technology Detections, News Events, Connections) via server-side push to a configured endpoint. | [link](https://docs.predictleads.com/) _primary_ | 2026-07-01 |
| ○ | rate_limit_rps | Not publicly published. A 'Request Limits' doc page exists but does not publish a per-second value. | — |  |
| ○ | rate_limit_day | Not published as a daily cap; usage is governed by a monthly credit quota (see pricing_model) rather than a documented per-day request limit. | — |  |
| ○ | concurrency | Not publicly published. | — |  |
| ○ | latency_p50_ms | Not published. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ⚠↓ | coverage | 120M+ companies globally with 30+ firmographic fields and daily updates; 50,000+ technologies tracked; 1.2B+ technology adoptions since 2018; 428M+ technology detections in the trailing year. | [link](https://predictleads.com/companies) _primary_ | 2026-07-01 |
| ○ | accuracy | No quantitative accuracy/precision metric published (only qualitative customer testimonials). | — |  |
| ✓✓ | pricing_model | Pay-as-you-go credit model. 100 free API credits/month, then tiered per-credit pricing with a $40/month minimum. 1 API request = 1 credit (regardless of records returned, up to 1,000); Discovery/Similar Companies billed per returned company; following a company = 1 credit/month per tracked company;… | [link](https://predictleads.com/pricing) _primary_ | 2026-07-01 |
| ✓✓ | price_per_match | Tiered per credit: $0.04 (101-5,000 credits/mo), $0.02 (5,001-10,000), $0.01 (10,001-100,000), $0.004 (100,001-500,000), $0.002 (500,001+). First 100 credits/month free. | [link](https://predictleads.com/pricing) _primary_ | 2026-07-01 |
| ✓ | error_codes | Standard HTTP status codes; responses carry a status_code field. HTTP 402 (Payment Required) is returned when the monthly credit limit is exceeded, with no overage charges incurred. A full published error-code table was not located. | [link](https://predictleads.com/pricing) _primary_ | 2026-07-01 |
| ○ | retry_behavior | No documented retry/backoff guidance (e.g. Retry-After header semantics) published. | — |  |
| ✓ | sdks | Python and Node.js client libraries; MCP server for AI agents/LLM workflows; Postman and Insomnia collections available. | [link](https://salestools.club/apis/predictleads) _secondary_ | 2026-07-01 |
| ✓ | api_versioning | URL-path versioning; current major version v3 (https://predictleads.com/api/v3/), docs labeled 'Documentation v3'. Individual dataset schemas are independently versioned (e.g. 3.4, 3.0). | [link](https://blog.predictleads.com/2024/08/21/introducing-predictleads-new-technology-detection-api-endpoint) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Global company coverage (worldwide, 120M+ companies). No region-specific API hosting or data-residency options documented; enterprise data delivery via AWS S3, GCS, or SFTP. | [link](https://predictleads.com/companies) _primary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 Type II attestation completed 2026-02-02 (audit by Johanson Group LLP; readiness via Koop.ai). Scope: security, availability, integrity, confidentiality, privacy. | [link](https://blog.predictleads.com/2026/02/12/predictleads-successfully-achieves-soc-2-compliance) _primary_ | 2026-07-01 |
| ○ | iso27001 | No ISO 27001 certification referenced on the SOC 2 announcement or other pages. | — |  |
| ✓ | gdpr | GDPR-aligned: privacy policy grants data-subject rights (access, rectification, erasure, restriction, objection, data portability) with a one-month response window; a Data Processing Agreement (DPA) is available; PredictLeads states it only tracks publicly available company data. | [link](https://predictleads.com/privacy) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA-aligned: California consumers may request disclosure of categories/specific personal data collected, deletion of personal data, and opt-out of sale of personal data. | [link](https://predictleads.com/privacy) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `coverage` (no: Citation mismatch. The cited /companies page supports only 120M+ companies, 30+ data fiel…)

- **Waterfall placement:** Not a contact/identity waterfall participant — PredictLeads returns NO work_email, personal_email, phone, or linkedin_url, so it never competes for those fields. Place it in the SIGNALS / INTENT + TECHNOGRAPHICS enrichment lane, running in PARALLEL to (and independent of) the email/phone waterfalls, keyed on an already-resolved company_domain. Per ADR-0007 reservation-value thinking it holds HIGH reservation value as a FIRST-CALL specialist for three field groups on companies already in the graph: (1) intent_topics / hiring signals (Job Openings dataset), (2) technographics (Technologies dataset — 50k+ technologies, strong depth), and (3) funding_stage / company news events (News Events dataset). Global region (no data-residency constraints). It is a weak/parallel secondary for generic firmographics (employee_count, industry, company_domain) where dedicated firmographic providers (e.g. broader B2B DBs) usually win — do not spend a PredictLeads credit as the primary firmographic call. Trigger placement: fire PredictLeads only after a company is identified, and prefer webhook subscriptions or flat-file sync for high-volume signal refresh to avoid per-request credit burn (1 request = 1 credit). Not a fallback for any contact field.

### G2 Buyer Intent
- **Category:** intent data
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** company_domain, employee_count, industry, intent_topics
- **Summary:** G2 Buyer Intent is a legitimate FIRST-PARTY intent-data API (not a scraper): G2 owns the behavioral data generated on its own marketplace properties (G2.com, Capterra, Software Advice, GetApp), so it is API-first and ADR-0002-compliant. It exposes company/account-level buyer-intent signals (9 signal types, buying stage, activity level) — NOT person-level contact data. Access is via the G2 Developer Portal: REST/JSON v2 API (base https://data.g2.com/api/v2/, Swagger reference at /api/v2/docs/index.html), buyer-intent via a market_signals endpoint over a date range, plus bulk delivery through B…
- **Adversarial verify:** 8 sampled → 6 confirmed, 2 downgraded. Two fabricated-looking specifics detected. (1) api_rest: the cited developer-portal page documents the base URL (https:…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Bearer access token generated in G2 Developer Portal (Access Tokens tab; tokens expire 1 year after creation); OAuth 2.0 also supported for third-party apps requiring user authorization/refresh tokens | [link](https://documentation.g2.com/docs/developer-portal) _primary_ | 2026-07-01 |
| ⚠↓ | api_rest | REST/JSON API. Base URL https://data.g2.com/api/v2/ with interactive Swagger reference at https://data.g2.com/api/v2/docs/index.html; buyer-intent signals retrieved via a GET market_signals endpoint for a date range | [link](https://documentation.g2.com/docs/developer-portal) _primary_ | 2026-07-01 |
| ✓✓ | api_versioning | Path-based versioning; current version is v2 (/api/v2/) | [link](https://documentation.g2.com/docs/developer-portal) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | No REST bulk/batch match endpoint documented. Bulk delivery is offered instead as a data share of the Buyer Intent table via Google BigQuery (Google Analytics Hub) and Snowflake; each row is a single interaction event (~35 columns). No documented max batch size | [link](https://documentation.g2.com/docs/bigquery-buyer-intent-data-dictionary) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Asynchronous delivery is via the BigQuery/Snowflake data share (shared dataset) rather than a REST async batch-job API; no async job-submit/poll API is documented | [link](https://documentation.g2.com/docs/bigquery-buyer-intent-data-dictionary) _primary_ | 2026-07-01 |
| ○ | pagination | Not documented in public developer materials | — |  |
| ✓✓ | webhooks | No public developer webhook API documented. Near-real-time signal routing is provided via 'Intent Studio' to connected integrations plus email notifications, not a subscribable webhook endpoint | [link](https://documentation.g2.com/docs/buyer-intent) _primary_ | 2026-07-01 |
| ○ | rate_limit_rps | Not published | — |  |
| ○ | rate_limit_day | Not published | — |  |
| ○ | concurrency | Not published | — |  |
| ○ | latency_p50_ms | Not published (G2 does not publish API latency figures) | — |  |
| ○ | latency_p95_ms | Not published (G2 does not publish API latency figures) | — |  |
| ✓✓ | coverage | Company/account-level intent (de-anonymized to organization, not person). Covers research activity across the G2 network: G2, Capterra, Software Advice, and GetApp. 9 signal types (Profile, Pricing, Alternatives, Category, Compare, Sponsored content, Licensed content, Reference page, Competitive) w… | [link](https://documentation.g2.com/docs/buyer-intent) _primary_ | 2026-07-01 |
| ○ | accuracy | Company de-anonymization / match accuracy rate not published by G2 | — |  |
| ⚠↓ | pricing_model | Annual subscription; Buyer Intent is sold as an add-on to G2 seller / Marketing Solutions plans (custom quote via sales), not a usage-based or per-record model. Third-party marketplace data indicates intent add-ons commonly land in the ~$10K-$40K/yr range depending on category footprint and negotia… | [link](https://www.vendr.com/marketplace/g2) _secondary_ | 2026-07-01 |
| ○ | price_per_match | N/A - not priced per match/record; subscription model, no per-match price published | — |  |
| ○ | error_codes | Not documented in public developer materials | — |  |
| ○ | retry_behavior | Not documented | — |  |
| ✓ | sdks | No official client SDKs published; G2 provides an interactive Swagger/OpenAPI reference and prebuilt platform integrations (Salesforce, Marketo, Pipedrive, Demandbase, etc.) rather than language SDKs | [link](https://documentation.g2.com/docs/developer-portal) _primary_ | 2026-07-01 |
| ○ | regional_availability | API hosting/data-residency regions not documented; intent records include visitor_region/visitor_country fields but no stated regional API endpoints or residency options | — |  |
| ✓ | soc2 | SOC 2 Type 2 (and SOC 3) listed on the G2 Trust Center | [link](https://trust.g2.com/) _primary_ | 2026-07-01 |
| ✓ | iso27001 | NOT held / not listed. G2 Trust Center lists SOC 2 Type 2, SOC 3, and CSA STAR; ISO 27001 and ISO 27701 are not listed | [link](https://trust.g2.com/) _primary_ | 2026-07-01 |
| ✓ | gdpr | GDPR compliant (listed on Trust Center); also Privacy Shield Verified | [link](https://trust.g2.com/) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA and CPRA compliant (listed on Trust Center) | [link](https://trust.g2.com/) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `api_rest` (no: Base URL https://data.g2.com/api/v2/ and Swagger reference at /api/v2/docs/index.html ARE…); `pricing_model` (no: Page supports annual subscription and Buyer Intent as an add-on to Professional/Enterpris…)

- **Waterfall placement:** Not a contact-fill (email/phone) waterfall participant. G2 Buyer Intent is an ACCOUNT-LEVEL INTENT SIGNAL source keyed on company_domain, used to score/prioritize accounts rather than to resolve emails, phones, or person identities. Per ADR-0007 reservation-value thinking it has HIGH reservation value for the intent_topics field because it is unique, proprietary G2/Capterra/Software-Advice/GetApp research data that no other provider can replicate — so it should run as a FIRST/SOLE (parallel) enrichment for intent_topics and company-level buying-stage/activity-level, not gated behind a cheaper fallback. Company firmographics it also returns (company_domain, employee_count, industry) are low reservation value and should defer to primary firmographic providers. Region: global coverage but strongest for US and English-language B2B software buyers; best used as the intent layer for US/NA and EMEA software-category ABM. Recommended ordering: trigger G2 intent lookups in parallel with (not blocking) the contact waterfall, then use the returned buying-stage/activity-level to rank which resolved contacts to action first.

### Melissa
- **Category:** identity resolution + address/contact verification
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** personal_email, mobile_phone, office_phone, email_status, phone_status
- **Summary:** Melissa (melissa.com / melissadata.net) is a long-established, API-first data-quality vendor offering identity resolution (Personator Identity), global address verification, and email/phone/name verification and append via synchronous REST web services (JSON & XML). It is a legitimate licensed-data API provider — NOT a scraper — so it passes ADR-0002 (API-first, no scraping). Note: Melissa is a registered California data broker (registration #186596), which is normal for a licensed identity/address data provider and reflects data-licensing, not ToS-violating scraping. Core verified outputs ma…
- **Adversarial verify:** 8 sampled → 2 confirmed, 6 downgraded. Multiple citation problems flagged. (1) price_per_match: the prepaid packs ($40=10k, $350=100k, $3,200=1M) and the '$12…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | License key — a 'license string' issued by a Melissa account representative, passed as a request parameter (typically the 'id' param) on the web-service call. | [link](https://github.com/MelissaData/Personator-Javascript) _primary_ | 2026-07-01 |
| ⚠↓ | api_rest | REST web service returning JSON or XML. Personator/Personator Identity accessed 'through REST'; Global Address base endpoint https://web-gae.melissadata.net/web/GlobalAddress/doGlobalAddress. Official Postman workspace 'Melissa Web API Samples' documents the calls. | [link](https://github.com/MelissaData/Personator-Javascript) _primary_ | 2026-07-01 |
| ⚠↓ | bulk_api | Yes — Melissa web/List services accept multiple records (batch) of JSON in a single call for verification/enrichment. Documented maximum batch size per request is not published on public sources (commonly cited operationally as up to 100 records, but not verified here). | [link](https://www.mssqltips.com/sqlservertip/7419/data-verification-enrichment-cleaner-data/) _secondary_ | 2026-07-01 |
| ○ | batch_async | No asynchronous/job-based batch API documented; web services are synchronous request/response. Large-file batch is handled via separate on-prem/desktop products, not an async REST job API. | — |  |
| ○ | pagination | Not applicable / not documented — single- or multi-record verify returns all records inline; no cursor/offset pagination model published. | — |  |
| ○ | webhooks | No webhooks/callbacks documented; services are synchronous REST only. | — |  |
| ○ | rate_limit_rps | Not published. | — |  |
| ○ | rate_limit_day | Not published as an rps/day cap; usage is governed by purchased/prepaid credit balance rather than a documented daily rate limit. | — |  |
| ○ | concurrency | Not published. | — |  |
| ○ | latency_p50_ms | Not published (real-time web service; no official latency SLA figures found). | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ⚠↓ | coverage | Global address verification across 240+ countries/territories (US, Canada, UK, EU, Australia, New Zealand, etc.); plus global email and phone verification and name/DOB/national-ID identity matching. | [link](https://www.g2.com/products/melissa-global-address-verification/pricing) _secondary_ | 2026-07-01 |
| ✓✓ | accuracy | Address accuracy backed by USPS CASS certification and SERP certification; no numeric accuracy-rate SLA published. | [link](https://syncgtm.com/blog/melissa-review) _secondary_ | 2026-07-01 |
| ⚠↓ | pricing_model | Credit/consumption-based: prepaid credit packs and volume tiers, plus annual subscriptions. Free trial available with no credit card (1,000 complimentary credits). One credit ≈ one verification. | [link](https://syncgtm.com/blog/melissa-review) _secondary_ | 2026-07-01 |
| ⚠↓ | price_per_match | Global Address Verification ≈ $0.003 per verification at entry tier (Tier1 $30 / 10,000 credits), declining to ≈ $0.00279 at 500k volume; annual global plan ≈ $12,600/yr for ~1,000,000 records. Prepaid packs e.g. $40=10k, $350=100k, $3,200=1M. | [link](https://syncgtm.com/blog/melissa-review) _secondary_ | 2026-07-01 |
| ⚠↓ | error_codes | Returns structured result/status codes rather than only HTTP errors. Personator Identity uses KV match codes (e.g. KV01 full address match, KV03/KV04 name, KV05 phone, KV07 DOB); Global Address returns AV/AS/GS-style address result codes indicating verification level and corrections. | [link](https://www.mssqltips.com/sqlservertip/7419/data-verification-enrichment-cleaner-data/) _secondary_ | 2026-07-01 |
| ○ | retry_behavior | No documented retry/idempotency policy for the web service; not published. | — |  |
| ✓ | sdks | Official sample code / lightweight wrappers on Melissa's GitHub org (github.com/MelissaData) — e.g. Personator HTML/JavaScript sample — plus an official Postman collection ('Melissa Web API Samples'). These are samples across multiple languages rather than fully packaged SDKs. | [link](https://github.com/MelissaData/Personator-Javascript) _primary_ | 2026-07-01 |
| ○ | api_versioning | Personator web service historically carries a version path segment (e.g. /v3/) but a public, authoritative versioning/deprecation policy was not verified in this pass. | — |  |
| ✓ | regional_availability | Global service (240+ countries covered). Available as cloud/Web API and also as on-premises/desktop deployments; no single-region hosting restriction documented. | [link](https://www.g2.com/products/melissa-global-address-verification/pricing) _secondary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 audited: Melissa completed a SOC 2 (Type 1) audit (publicly announced 2015); SOC2 is listed among the compliance frameworks covered by paid plans. Current Type 2 attestation date not verified from a primary source. | [link](https://www.softwaremag.com/melissa-data-awarded-certification-in-service-organization-controls-2-soc2/) _secondary_ | 2026-07-01 |
| ○ | iso27001 | No ISO 27001 certification found in public sources. | — |  |
| ✓ | gdpr | GDPR compliance supported (listed among compliance reporting for paid plans); Melissa honors data-subject rights (right to know, access, erasure, and to withdraw consent). | [link](https://privacyhawk.com/resources/how-to-opt-out-delete-or-make-privacy-requests-from-melissa) _secondary_ | 2026-07-01 |
| ✓ | ccpa | CCPA compliant: registered California data broker (CA OAG registration #186596). Provides Right-to-Know / Opt-Out-of-Sale / Right-to-Delete via web form, CCPArequest@melissa.com, or phone; CCPA listed in paid-plan compliance reporting. | [link](https://oag.ca.gov/data-broker/registration/186596) _secondary_ | 2026-07-01 |

**Downgraded on re-check:** `api_rest` (no: Page loaded and supports only the 'through REST' part (verbatim: 'It is written in HTML a…); `bulk_api` (no: Cited URL returns HTTP 403 on direct fetch; article content recovered via search. The art…); `coverage` (unreachable: The exact cited page (g2.com/products/melissa-global-address-verification/pricing) return…); `pricing_model` (no: Page supports credit-based consumption with prepaid volume tiers and a free trial with no…); `price_per_match` (no: Per-verification tier figures ARE supported: Tier1 '$30 for 10,000 credits. $0.003 per ve…); `error_codes` (no: Cited URL returns HTTP 403 on direct fetch; content recovered via search. The article sup…)

- **Waterfall placement:** Verification/validation stage — not a primary B2B contact-discovery source. (1) FIRST/primary for postal-address standardization and verification and as a PARALLEL verifier for email_status and phone_status confirmation across ALL regions — Melissa's global reach (240+ countries, CASS/SERP-certified) makes it the strongest node for international address + contact validation where B2B providers are weak (EU/APAC/LATAM). (2) FALLBACK for personal_email / mobile_phone / office_phone append after dedicated B2B people-data providers, using KV match codes to gate confidence. (3) Do NOT place Melissa in the path for work_email, direct_dial, linkedin_url, job_title, company_domain, employee_count, industry, technographics, intent_topics, or funding_stage — outside its wheelhouse. Per ADR-0007 reservation-value thinking, reserve Melissa credits (billed per verification) for high-value identity-match confirmation (name+address+phone+email KV codes) and international address regions rather than as a first-touch enrichment for firmographic/B2B fields; per ADR-0004/0007/0008 order it after cheaper cache/primary contact sources and before manual review.

### Ekata (Mastercard)
- **Category:** identity verification / risk
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** email_status, phone_status
- **Summary:** Ekata (a Mastercard company) is a legitimate API-first identity verification and transaction-risk provider — NOT a scraper. Its data comes from the proprietary Ekata Identity Engine (Ekata Identity Graph + Ekata Identity Network), derived from billions of licensed/real-time transaction signals, so it is compatible with our API-first, no-scraping charter (ADR-0002). It exposes REST/JSON APIs (Account Opening, Transaction Risk, Identity Check, plus Address/Phone/Reverse-lookup APIs) that take up to two sets of inputs (email, phone, name, physical address, IP) and return validity/match signals a…
- **Adversarial verify:** 8 sampled → 4 confirmed, 4 downgraded. Reliability concerns by source: (1) regional_availability cites about-fraud.com/providers/ekata, but that page says NOT…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Mastercard Developers uses one-legged OAuth 1.0a with per-request digital signing via a PKCS#12 (.p12) keystore + consumer/client key; legacy Whitepages Pro used a static API key parameter. | [link](https://developer.mastercard.com/platform/documentation/authentication/using-oauth-1a-to-access-mastercard-apis/) _primary_ | 2026-07-01 |
| ⚠↓ | api_rest | Yes — REST/JSON APIs with OpenAPI/Swagger spec. Suite hosted on Mastercard Developers (developer.mastercard.com/ekata-product-suite); legacy base URL proapi.whitepages.com/3.2/ (e.g. /3.2/identity_check). Exact current api.mastercard.com host path not confirmed from a fetched body. | [link](https://apitracker.io/a/ekata) _secondary_ | 2026-07-01 |
| ○ | bulk_api | No documented bulk/multi-record endpoint or max batch size. Identity Check accepts up to two input sets (primary + secondary identity) per single query, which is not a bulk API. | — |  |
| ○ | batch_async | No public evidence of an asynchronous/batch job API; APIs are synchronous request/response. | — |  |
| ○ | pagination | Not applicable / not documented — single-record lookup APIs with no list endpoints. | — |  |
| ○ | webhooks | No webhooks documented; interaction is synchronous request/response only. | — |  |
| ○ | rate_limit_rps | Not publicly published (contract/tier-dependent). | — |  |
| ○ | rate_limit_day | Not publicly published (contract/tier-dependent). | — |  |
| ○ | concurrency | Not publicly published. | — |  |
| ○ | latency_p50_ms | Not published as a p50 percentile. Vendor markets the Transaction Risk API at 'response time under 100 ms' (marketing figure, not a measured percentile). | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ✓✓ | coverage | Global coverage across 5 core identity attributes: email, phone, name (person/business), physical address, IP — sourced from billions of real-time transactions in the Ekata proprietary network. Identity Check returns 70+ data signals per query. | [link](https://www.about-fraud.com/providers/ekata/) _secondary_ | 2026-07-01 |
| ○ | accuracy | No published, independently-verifiable accuracy/match-rate percentage. | — |  |
| ⚠↓ | pricing_model | Custom / usage-based; not publicly disclosed — contact sales for a volume/use-case-based quote. | [link](https://apitracker.io/a/ekata) _secondary_ | 2026-07-01 |
| ○ | price_per_match | Not publicly disclosed. | — |  |
| ○ | error_codes | Not confirmed from a fetched source (legacy Whitepages Pro used standard HTTP status + error objects; current Mastercard platform returns standard HTTP error codes) — no live doc body verified. | — |  |
| ○ | retry_behavior | No published retry/idempotency guidance verified. | — |  |
| ⚠↓ | sdks | SDKs available per API aggregator (Mastercard Developers provides OAuth1 signing SDKs); specific languages/versions not detailed in a fetched source. | [link](https://apitracker.io/a/ekata) _secondary_ | 2026-07-01 |
| ○ | api_versioning | URL path versioning historically (legacy Whitepages Pro v3.2, e.g. /3.2/identity_check); current Mastercard Developers product versioning not confirmed from a live doc body. | — |  |
| ⚠↓ | regional_availability | Global availability; EU/EEA served with GDPR-compliant options (Ekata acts as data Controller, BCRs/DPA in place); US served with a CCPA addendum. | [link](https://www.about-fraud.com/providers/ekata/) _secondary_ | 2026-07-01 |
| ✓✓ | soc2 | SOC 2 attested (AICPA SOC 2 — security, confidentiality, availability, privacy). | [link](https://security-profiles.nudgesecurity.com/app/ekata-com) _secondary_ | 2026-07-01 |
| ✓✓ | iso27001 | ISO 27001 certified/compliant. | [link](https://security-profiles.nudgesecurity.com/app/ekata-com) _secondary_ | 2026-07-01 |
| ✓ | gdpr | GDPR compliant; Ekata acts as data Controller for personal info subject to GDPR, with a Data Processing Agreement and Binding Corporate Rules (BCRs). | [link](https://ekata.com/ekata-agreements-and-terms/dpa-bcrs/) _primary_ | 2026-07-01 |
| ✓ | ccpa | CCPA compliant — a CCPA Addendum forms part of the customer agreement; consumer-rights contact provided (ekataprivacyanddataprotection@mastercard.com / +1 855-927-1072). | [link](https://ekata.com/ekata-agreements-and-terms/dpa-bcrs/) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `api_rest` (no: The apitracker page mentions an 'OpenAPI/Swagger specification' generically, but does NOT…); `pricing_model` (no: The apitracker page's 'API pricing' field shows only a dash ('—'), meaning no pricing is …); `sdks` (no: The apitracker page exposes only an 'SDKs' tab link (/a/ekata/sdks) with no SDK details, …); `regional_availability` (no: The about-fraud.com Ekata page contains NO mention of GDPR, EU/EEA regional handling, Eka…)

- **Waterfall placement:** Not a contact-discovery source — do NOT place Ekata in the email/phone DISCOVERY waterfall. Per ADR-0007 reservation-value thinking, Ekata's reservation value is highest as a downstream VERIFICATION / RISK-SCORING stage that runs AFTER contact fields have been discovered by other providers. Placement: (1) email_status and phone_status validation/risk enrichment — run in PARALLEL with or as a FALLBACK confirmation to dedicated deliverability/HLR providers, using Ekata's Identity Check / Transaction Risk signals to raise confidence and flag high-risk identities; (2) as the primary engine for a separate FRAUD/KYC/identity-risk waterfall (account opening, transaction risk) rather than the B2B contact-enrichment waterfall. Region: global, with particularly strong US and EU coverage (GDPR-controller posture makes it safe for EU-subject processing); use it first for EU/US identity-risk checks. Because rate limits, per-match price, and latency percentiles are unpublished, treat cost/latency as contract-negotiated and gate calls behind a hit only when an identity-risk or email/phone-status decision is actually needed (avoid speculative fan-out).

### WhoisXML API
- **Category:** domain intelligence
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** company_domain, domain_registration_metadata_whois (registrant org, created/updated/expires dates, registrar, nameservers), domain_age, dns_mx_records_presence (deliverability signal), ip_netblocks, subdomains, domain_reputation_risk_score
- **Summary:** WhoisXML API is a legitimate, API-first domain/WHOIS/DNS/IP intelligence provider (not a scraper) that aggregates registry, registrar, and RDAP data plus DNS/IP telemetry and exposes them via RESTful HTTPS APIs (JSON/XML). It is API-first and compatible with ADR-0002 (no scraping). For a contact-enrichment waterfall it does NOT return people fields (emails/phones/titles); its value is domain-level intelligence: it validates/normalizes company_domain, returns domain registration/age metadata, DNS/MX presence (a deliverability signal), IP netblocks, subdomains, and domain reputation/risk. Auth …
- **Adversarial verify:** 8 sampled → 5 confirmed, 3 downgraded. Several citations are weaker than the claimed values imply. (1) rate_limit_rps: the '500 requests/minute per product' E…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | API key via apiKey query parameter (highest priority); also Server-to-Server (two-legged) OAuth 2.0 Bearer token, token endpoint https://main.whoisxmlapi.com/oauth/token | [link](https://whois.whoisxmlapi.com/documentation/making-requests) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | RESTful HTTPS API supporting GET and POST; base URL https://www.whoisxmlapi.com/whoisserver/WhoisService; responses in JSON or XML via outputFormat param (default XML) | [link](https://whois.whoisxmlapi.com/documentation/making-requests) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — Bulk WHOIS API; bulk WHOIS lookup supports up to 500,000 domains per job at 1 credit per domain; results stored for 1 year (max batch = 500,000 domains) | [link](https://whois.whoisxmlapi.com/bulk-whois-lookup) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes — asynchronous: POST domain array to https://www.whoisxmlapi.com/BulkWhoisLookup/bulkServices/bulkWhois returns a requestId; client then polls request status and downloads results; results stored 1 year | [link](https://whois.whoisxmlapi.com/bulk-api/documentation/making-requests) _primary_ | 2026-07-01 |
| ○ | pagination | Not confirmed in fetched docs; core WHOIS API is single-record lookup. Reverse WHOIS / discovery APIs return multi-result sets but paging mechanics not verified from a fetched source | — |  |
| ○ | webhooks | No webhooks documented; async bulk retrieval is poll-based via requestId status polling (absence not directly citable) | — |  |
| ⚠↓ | rate_limit_rps | WHOIS API standard limit is 50 requests/second (over-limit requests rejected until next second). Enterprise API Packages limited to 500 requests/minute per product; dedicated load balancer / premium endpoint available for higher throughput | [link](https://whois.whoisxmlapi.com/documentation/limits) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No fixed per-day request cap published; consumption governed by monthly credit/subscription quotas rather than a daily ceiling | — |  |
| ○ | concurrency | No explicit concurrency limit published; throughput governed by the 50 rps rate limit | — |  |
| ○ | latency_p50_ms | Not published (recommended client service timeout is 100 seconds, which is a timeout, not a latency SLA) | — |  |
| ○ | latency_p95_ms | Not published | — |  |
| ✓✓ | coverage | WHOIS API tracks 374M+ active domains across 7,596 TLDs; repository of 28.7B+ historical domain WHOIS records; global IP/DNS/subdomain coverage across the product portfolio | [link](https://whois.whoisxmlapi.com/) _primary_ | 2026-07-01 |
| ○ | accuracy | No published accuracy/match-rate metric for WHOIS/domain data | — |  |
| ⚠↓ | pricing_model | Credit + subscription based; one-time, monthly, and annual plans; charged in API credits (WHOIS API) or DRS credits (Domain Research Suite); free tier = first 500 WHOIS API calls complimentary and a 500 DRS-credit free subscription | [link](https://whois.whoisxmlapi.com/documentation/making-requests) _primary_ | 2026-07-01 |
| ⚠↓ | price_per_match | Per-request credit cost: standard WHOIS lookup = 1 credit, hard refresh = 5 credits; Bulk WHOIS = 1 credit/domain; Reverse WHOIS = 1 DRS credit; Subdomains Lookup = 10 DRS credits; WHOIS History = 50 DRS credits. Per-credit USD price is plan/volume dependent and not publicly listed | [link](https://drs.whoisxmlapi.com/pricing) _primary_ | 2026-07-01 |
| ✓ | error_codes | Documented application error codes returned with code + text description, e.g. WHOIS_02 'User is not logged in', WHOIS_03 'Unable to retrieve whois record for $domainName', DB_01 authentication/database error; responses also carry a messageCode field; standard HTTP status semantics | [link](https://whois.whoisxmlapi.com/documentation/error-handling) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | Requests exceeding the 50 rps limit are rejected until the next second (client must throttle/retry); recommended client service timeout is 100 seconds; no documented server-side automatic retry or Retry-After backoff | [link](https://whois.whoisxmlapi.com/documentation/limits) _primary_ | 2026-07-01 |
| ✓ | sdks | Official client libraries and code samples in multiple languages (Python, PHP, Ruby, Perl, C#, PowerShell, Java, Node.js) via per-product developer-libraries pages and the official GitHub org github.com/whois-api-llc | [link](https://whois.whoisxmlapi.com/integrations/developer-libraries/python) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Per-product URL-path versioning across the portfolio (e.g., /api/v1, /v2, /v3 on newer APIs such as Email Verification v2 and Website Categorization v3); the core WHOIS API endpoint (whoisserver/WhoisService) is unversioned | [link](https://website-categorization.whoisxmlapi.com/api/documentation/v3/making-requests) _primary_ | 2026-07-01 |
| ○ | regional_availability | Global SaaS availability; premium tier offers dedicated load balancer / premium endpoint, but no specific regional data-residency options are published | — |  |
| ○ | soc2 | Claimed 'SOC 2 Compliant' only by a third-party vendor security profile (Nudge Security); not confirmed on any WhoisXML primary trust/security page | [link](https://security-profiles.nudgesecurity.com/app/whoisxmlapi-com) _secondary_ |  |
| ○ | iso27001 | Claimed 'ISO 27001 Compliant' only by a third-party vendor security profile (Nudge Security); not confirmed on any WhoisXML primary trust/security page | [link](https://security-profiles.nudgesecurity.com/app/whoisxmlapi-com) _secondary_ |  |
| ✓ | gdpr | Addressed: Privacy Policy is subject to GDPR and the company participates in EU-U.S./Swiss-U.S. Privacy Shield; ToS §28.2 requires customers to execute a separate Data Processing Addendum governing processing of Personal Data under GDPR | [link](https://main.whoisxmlapi.com/terms-of-service) _primary_ | 2026-07-01 |
| ✓ | ccpa | Addressed: ToS §28.7 states 'Whois API does not sell personal information provided through the Services, as sell is defined in the CCPA' and that personal information is processed solely for a CCPA business purpose; customer warrants CCPA compliance | [link](https://main.whoisxmlapi.com/terms-of-service) _primary_ | 2026-07-01 |

**Downgraded on re-check:** `rate_limit_rps` (no: Partially supported, so downgraded. The page confirms 'The maximum number of requests per…); `pricing_model` (no: Weak/mismatched citation, downgraded. The cited making-requests page only supports the fr…); `price_per_match` (no: Downgraded. Two separate fetches of the cited DRS pricing page found NO numeric per-reque…)

- **Waterfall placement:** Not part of the email/phone contact waterfall (returns no people PII). Runs in a PARALLEL domain-intelligence branch keyed on company_domain. Per ADR-0007 reservation value: FIRST-line, cheap (1-credit) domain gate/enricher for domain validation, domain-age/registration metadata, and MX/DNS presence (deliverability pre-check before spending on email-finder providers); domain reputation/risk scoring to drop fraudulent/low-value domains early; reverse-WHOIS to resolve an org to its domain portfolio. Global coverage means no region-specific fallback. NOT a fallback for work_email/personal_email/mobile_phone/direct_dial/linkedin_url/job_title/employee_count — it cannot supply those fields.

### Clearout
- **Category:** email verification + finder
- **Status:** ACTIVE-CANDIDATE
- **Capabilities:** email_status, work_email, company_domain, job_title, linkedin_url, mobile_phone, employee_count, industry
- **Summary:** Clearout is a legitimate API-first email verification + email finder / prospecting provider (SMTP/MX-based real-time and bulk verification plus a first-party prospect database), NOT a scraper. Its REST API (base https://api.clearout.io/v2, Bearer-JWT auth) offers instant and asynchronous bulk verification, webhooks, per-minute rate limiting with rate-limit headers, path-based v2 versioning, and documented HTTP + app error codes. Compliance is strong and current: SOC 2 Type II (renewed 2026-03-19), ISO 27001:2022 (renewed 2026-01-23), ISO 27701:2019, and GDPR (both Processor and Controller). P…
- **Adversarial verify:** 8 sampled → 7 confirmed, 1 downgraded. One fabricated-looking citation: the webhooks claim cites https://docs.clearout.io/development/api-overview.html, which…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Bearer token (JWT). Header: `Authorization: Bearer <TOKEN>`. Token created in dashboard Developer → API → Create API Token. | [link](https://docs.clearout.io/developers/api/overview.md) _primary_ | 2026-07-01 |
| ✓✓ | api_rest | Yes — REST/JSON API. Base URL https://api.clearout.io/v2 (region-specific base URLs also issued; check Developer → Reference in the app). Example endpoint POST /email_verify/instant. Note: no browser/client-side CORS requests — server-side only. | [link](https://docs.clearout.io/developers/api/email-verify) _primary_ | 2026-07-01 |
| ✓✓ | bulk_api | Yes — POST /email_verify/bulk accepts CSV/XLS/XLSX file upload (multipart/form-data), returns list_id. No strict documented max file/batch size; docs recommend splitting very large files (100k+ records) into multiple batches. 'optimize' param = highest_accuracy \| fastest_turnaround. | [link](https://docs.clearout.io/developers/api/email-verify) _primary_ | 2026-07-01 |
| ✓✓ | batch_async | Yes — bulk is asynchronous: upload → poll GET /email_verify/bulk/progress_status?list_id=X → POST /download/result. Optional webhook on completion to avoid polling. | [link](https://docs.clearout.io/developers/api/email-verify) _primary_ | 2026-07-01 |
| ○ | pagination | Not documented in the developer API reference. | — |  |
| ⚠↓ | webhooks | Yes — webhooks send HTTP POST with JSON payload to your HTTPS endpoint when events complete (email verification, email finder, Form Guard), allowing avoidance of polling. | [link](https://docs.clearout.io/development/api-overview.html) _primary_ | 2026-07-01 |
| ✓✓ | rate_limit_rps | Not published as requests-per-second. Enforced per 60-second window with x-ratelimit-limit / x-ratelimit-remaining / x-ratelimit-reset headers. Per-minute tiers: Email Verify 25–400 RPM (subscription) / 20–320 RPM (PAYG); Email Finder 14–240 RPM (subscription) / 10–190 RPM (PAYG), by credit tier; d… | [link](https://docs.clearout.io/developers/api/overview.md) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No daily request cap documented; throughput is effectively bounded by per-minute RPM tiers and available credits. | — |  |
| ○ | concurrency | No explicit concurrency limit documented (governed by per-minute RPM window). | — |  |
| ○ | latency_p50_ms | Not published. Instant-verify request supports a client timeout (default 130000 ms, max 180000 ms) but no p50 latency is stated. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ✓✓ | coverage | Domain-agnostic email verification — validates any email/domain (catch-all, disposable, role-based, free, greylisting, spam-trap detection) via 20+ checks (SMTP ping, MX lookup, syntax, mailbox quota). Email Finder + Prospect draw on a first-party database. Vendor-stated scale: 80,000+ customers. N… | [link](https://clearout.io/email-verifier/) _primary_ | 2026-07-01 |
| ✓✓ | accuracy | Vendor-stated up to 99.73% accuracy across all email types; guarantees recipient bounce rate stays below 3%. (Vendor marketing claim, not independently audited.) | [link](https://clearout.io/email-verifier/) _primary_ | 2026-07-01 |
| ✓ | pricing_model | Credit-based prepaid. Plans: Monthly subscription, Annual subscription (up to ~20% discount), and one-time / Pay-As-You-Go. Credit cost per action: email verify = 1 credit; email finder = 4 credits (exact match) / 2 credits (alternative); prospecting = 4 credits/email, 2 credits/phone. 100 free cre… | [link](https://clearout.io/pricing/) _primary_ | 2026-07-01 |
| ✓ | price_per_match | In credits (primary): verify 1 credit, exact finder match 4 credits. Approx USD: PAYG entry ~$21 / 3,000 credits ≈ $0.007/credit → ~$0.007 per verified email, ~$0.028 per exact finder match (USD/credit figure from secondary source; exact per-credit $ not on Clearout's public pricing page). | [link](https://mentionagent.ai/blog/clearout-pricing/) _secondary_ | 2026-07-01 |
| ✓ | error_codes | Yes — HTTP status codes 200, 400 (validation), 401 (auth), 402 (insufficient credits), 429 (rate limit), 503, 524 (timeout); plus application error codes (1001, 1002, 1004, 1028, 1030, 1031, 1032, …) and verification sub-status codes (200, 400–421, 601–604, 701). | [link](https://docs.clearout.io/developers/api/email-verify) _primary_ | 2026-07-01 |
| ✓ | retry_behavior | On HTTP 429, clients should wait until the duration in the x-ratelimit-reset header elapses before retrying (60-second window). No auto-retry/backoff behavior specified server-side. | [link](https://docs.clearout.io/developers/api/overview.md) _primary_ | 2026-07-01 |
| ✓ | sdks | Node.js SDK (server-side) and a JavaScript widget (client-side). Copy-paste code snippets provided for cURL, PHP, Java, Node.js, Python, C#, Go, and R. | [link](https://docs.clearout.io/development/api-overview.html) _primary_ | 2026-07-01 |
| ✓ | api_versioning | Path/URL-based versioning; current version v2 (e.g., /v2/email_verify/…). | [link](https://docs.clearout.io/developers/api/email-verify) _primary_ | 2026-07-01 |
| ✓ | regional_availability | Region-specific API base URLs are issued per account (default https://api.clearout.io/v2); users locate their endpoint via Developer → Reference. Configurable data retention (default 30 days, adjustable 1–45 days). | [link](https://docs.clearout.io/developers/api/overview.md) _primary_ | 2026-07-01 |
| ✓ | soc2 | SOC 2 Type II — renewed 2026-03-19 (Trust Services Criteria: security, availability, processing integrity, confidentiality, privacy). | [link](https://clearout.io/security-compliance/) _primary_ | 2026-07-01 |
| ✓ | iso27001 | ISO/IEC 27001:2022 — renewed 2026-01-23. Also holds ISO/IEC 27701:2019 (privacy information management / PIMS). | [link](https://clearout.io/security-compliance/) _primary_ | 2026-07-01 |
| ✓ | gdpr | GDPR compliant — operates as both Data Processor and Data Controller. SSL encryption in transit and at rest, Okta SSO, annual third-party penetration tests, quarterly internal/external audits. | [link](https://clearout.io/security-compliance/) _primary_ | 2026-07-01 |
| ○ | ccpa | Not stated on the security & compliance page (SOC 2 / ISO / GDPR listed; CCPA and HIPAA not mentioned). | — |  |

**Downgraded on re-check:** `webhooks` (unreachable: Cited URL returns 404 / Page Not Found — the page does not exist, so it cannot support th…)

- **Waterfall placement:** Primary role = email VERIFICATION / validation stage (returns email_status), placed FIRST or in PARALLEL in the verification lane globally (domain-agnostic, ~$0.007/credit, vendor-stated 99.73% accuracy, below-3% bounce guarantee) — use it to validate/clean emails emitted by upstream finder providers before acceptance, and to gate deliverability. Secondary role = email FINDER FALLBACK (work_email, at 4 credits/exact) placed AFTER higher-hit-rate primary finders (e.g., specialist B2B finders) as a mid-tier catch, given per-match cost of ~4x a verify. Per ADR-0007 reservation-value thinking: its cheap, high-accuracy verification has high option value as an always-on validation step, so reserve budget for it there rather than as a first-call finder. Bulk async endpoint + webhooks suit batch/backfill jobs; server-side only (no client-side CORS). Strong, current SOC 2 Type II + ISO 27001:2022/27701 + GDPR (Processor & Controller) make it enterprise-safe for regulated regions; CCPA unconfirmed, so flag before US-California-specific data-subject workflows.

### Proxycurl / Nubela
- **Category:** linkedin person/company data (scraping-based B2B enrichment API)
- **Status:** EXCLUDED _(research agent said: EXCLUDED — EXCLUDED on two independent, hard grounds. (1) SCRAPING-BASED / ToS-VIOLATING (violates ADR-0002 — our engine is API-first, licensed data o…; Hard-excluded: LinkedIn litigation + service wind-down (legal + supply-continuity), not merely provenance. ADR-0009.)_
- **Capabilities:** linkedin_url, job_title, work_email, company_domain, employee_count, industry, funding_stage
- **Summary:** Proxycurl (operated by Nubela Pte Ltd, Singapore) was a credit-based REST API that resold LinkedIn person and company profile data ("fresh B2B data ... at scale"). Technically it was a clean, well-documented API (Bearer-token auth, HTTPS GET endpoints under https://nubela.co/proxycurl/api/v2/linkedin, official Python/JS SDKs, ~300 req/min limits, ~$0.01/credit) — but the underlying DATA was obtained by scraping LinkedIn, allegedly via hundreds of thousands of fake accounts. LinkedIn sued in January 2025; the case resolved by July 2025 with a court-entered PERMANENT INJUNCTION requiring Proxyc…
- **Adversarial verify:** 7 sampled → 4 confirmed, 3 downgraded. Provider is effectively defunct. Proxycurl was shut down in July 2025 after LinkedIn sued Nubela Pte. Ltd. (LinkedIn Co…

| ✔ | Attribute | Value | Source | Date |
|---|---|---|---|---|
| ✓✓ | auth | Static API key passed as Bearer token in HTTP Authorization header: 'Authorization: Bearer <API_KEY>' (key generated in dashboard). | [link](https://nubela.co/proxycurl/docs) _primary_ | 2026-07-01 |
| ⚠↓ | api_rest | REST over HTTPS, GET requests. Person Profile base URL: https://nubela.co/proxycurl/api/v2/linkedin (1 credit per successful call). Additional /proxycurl/api/... endpoints for company, employee search, jobs, email lookup. | [link](https://nubela.co/proxycurl/docs) _primary_ | 2026-07-01 |
| ○ | bulk_api | No dedicated bulk/batch enrichment endpoint found in available sources; enrichment is per-profile GET, parallelized by the client. | — |  |
| ○ | batch_async | No asynchronous batch/job-submission API documented. | — |  |
| ○ | pagination | Search-type endpoints reportedly return a next_page URL, but this could not be confirmed from a clean primary source (docs fetch was unreliable). | — |  |
| ○ | webhooks | No webhook/callback delivery mechanism found. | — |  |
| ⚠↓ | rate_limit_rps | 300 requests/minute (~5 rps), with burst up to 1500 requests per 5 minutes, per official documentation. | [link](https://nubela.co/proxycurl/docs) _primary_ | 2026-07-01 |
| ○ | rate_limit_day | No documented fixed daily request cap; effective daily throughput bounded by purchased credit balance / subscription tier. | — |  |
| ○ | concurrency | No explicit concurrent-connection limit documented; throughput governed by the per-minute and 5-minute burst limits. | — |  |
| ○ | latency_p50_ms | Not published. Live (non-cached) LinkedIn fetches were seconds-scale, but no p50 figure is cited by the vendor. | — |  |
| ○ | latency_p95_ms | Not published. | — |  |
| ○ | coverage | Marketing claimed hundreds of millions of LinkedIn person and company profiles; unverifiable, and the data is now court-ordered for permanent deletion. | — |  |
| ○ | accuracy | No published accuracy or match-rate metric. | — |  |
| ✓✓ | pricing_model | Credit-based. Monthly subscription tiers (approx. $49-$299/mo entry tiers up to higher-volume plans) plus non-expiring pay-as-you-go credits; 1 credit per successful person-profile enrichment. | [link](https://nubela.co/proxycurl/pricing.html) _primary_ | 2026-07-01 |
| ✓✓ | price_per_match | ~$0.01 per successful request (1 credit); high-volume/ala-carte credits as low as ~$0.009/credit. | [link](https://nubela.co/proxycurl/pricing.html) _primary_ | 2026-07-01 |
| ○ | error_codes | Standard HTTP status codes were used (e.g., 401 invalid key, 403 insufficient credits, 429 rate-limited, 5xx), but a clean primary enumeration could not be verified; the only detailed docs fetch returned corrupted/hallucinated content and was discarded. | — |  |
| ○ | retry_behavior | HTTP 429 on rate-limit with client-side backoff expected; not confirmable from a clean primary source. | — |  |
| ✓✓ | sdks | Official open-source SDKs: Python (proxycurl-py-linkedin-profile-scraper) and JavaScript/Node (proxycurl-js-linkedin-profile-scraper), published under the github.com/nubelaco organization. | [link](https://github.com/nubelaco/proxycurl-py-linkedin-profile-scraper) _primary_ | 2026-07-01 |
| ⚠↓ | api_versioning | Path-based versioning; both v1 and v2 exist (person profile served at /proxycurl/api/v2/linkedin). | [link](https://nubela.co/proxycurl/docs) _primary_ | 2026-07-01 |
| ○ | regional_availability | Single global endpoint (nubela.co); vendor Nubela Pte Ltd is Singapore-based. No regional data-residency options documented. Service is now defunct (shut down July 2025). | — |  |
| ○ | soc2 | No SOC 2 attestation published or found. | — |  |
| ○ | iso27001 | No ISO 27001 certification published or found. | — |  |
| ○ | gdpr | Vendor marketed data as collected 'legally, at scale,' but a US federal court entered a permanent injunction over unauthorized LinkedIn access via fake accounts, which fatally undermines any lawful-basis/GDPR compliance claim. No credible GDPR program verifiable. | — |  |
| ○ | ccpa | No verifiable CCPA compliance program; data provenance was ruled unlawful (see injunction). | — |  |

**Downgraded on re-check:** `api_rest` (no: REST-over-HTTPS/GET is accurate, but the cited endpoint structure is wrong/absent. The pa…); `rate_limit_rps` (no: Contradicted. The docs state: 'Paid API endpoints are limited to 50 requests per minute.'…); `api_versioning` (no: Path-based versioning is real and both v1 (/api/v1/...) and v2 (/api/v2/...) do exist on …)

- **Waterfall placement:** DO NOT PLACE in any waterfall tier. Absent the legal problems, Proxycurl would have been a candidate LinkedIn-derived enrichment source for person job_title / linkedin_url / employer + company employee_count / industry / funding_stage, likely a fallback (not first-call) enrichment step given seconds-scale live-fetch latency and per-credit cost. However, per ADR-0002 (API-first, no scraping) and ADR-0007 reservation-value thinking, its reservation value is effectively negative: the data provenance is court-ruled unlawful, the service is shut down, and any coverage it once had is now legally required to be deleted. For legitimate LinkedIn-shaped fields (title/company/seniority), route to licensed/compliant providers (e.g., People Data Labs, Coresignal-licensed feeds, or first-party enrichment) instead. Reserve zero waterfall slots for this provider in every region.

## 6. Exclusions & inclusion policy

Full rationale in **[ADR-0009](../adr/0009-provider-inclusion-exclusion-criteria.md)** (resolves the
"scraped-provenance ⇒ exclude" inconsistency vs. already-ACTIVE Apollo/ZoomInfo).

| Provider | Research-agent verdict | **Final (ADR-0009)** | Ground |
|----------|------------------------|----------------------|--------|
| Proxycurl / Nubela | EXCLUDED | **EXCLUDED** | LinkedIn litigation + service wind-down (legal + continuity) |
| Datanyze | EXCLUDED | **EXCLUDED** | ZoomInfo-absorbed; deprecated API, no standalone surface (continuity) |
| Kaspr | EXCLUDED | **DEPRIORITIZED** | LinkedIn-extension provenance → compliance-gated; **PR-EXCL-1** |
| ContactOut | EXCLUDED | **DEPRIORITIZED** | LinkedIn/crawl provenance → compliance-gated; **PR-EXCL-1** |
| Coresignal | EXCLUDED | **DEPRIORITIZED** | legit DaaS API; public-web provenance = same class as Apollo; **PR-EXCL-1** |
| Datagma | DEPRIORITIZED | **DEPRIORITIZED** | EU niche; usable behind cleaner sources |

## 7. Open items & Phase-2 question resolution

| ID | Item | Status |
|----|------|--------|
| **PR-EXCL-1** | Human policy decision on DEPRIORITIZED-with-LinkedIn-provenance (Kaspr, ContactOut, Coresignal) | **RESOLVED 2026-07-01** — human chose compliance-gated DEPRIORITIZED (ADR-0009) |
| PR-LAT-1 | All latency p50/p95 cells `UNVERIFIED` (vendors don't publish) | `ACCEPTED-RISK` → load test (`21`) |
| PR-IDV-1 | Identity/domain-intel providers heavily downgraded (Melissa/Ekata/Diffbot/WhoisXML) — specifics provisional | `ACCEPTED-RISK` → deepen before integrating each |
| PR-CB-1 | 38 downgraded claims now `UNVERIFIED` | `ACCEPTED-RISK` (honest gaps) |
| WQ-3 (P2) | Cold-start ordering | **partially resolved** — §3 seed order provided (replace with measured reservation values) |
| WQ-2 (P2) | Provider correlation / copy graph | **informed** — known reseller/ownership links noted (NeverBounce⊂ZoomInfo, Datanyze⊂ZoomInfo, Ekata⊂Mastercard); full graph pending measured data |
| WQ-5 (P2) | Freshness half-life per field | still open → `08`/`16` |

## 8. Reviewer result (`/gate-check` Phase 3)

**Architecture Reviewer + Gap-Analysis + Security-relevant checks, 2026-07-01.**

| Check | Result | Evidence |
|-------|--------|----------|
| Every claim cited or `UNVERIFIED` | **PASS** | 672 claims; each `source_url`+date or `○`; 38 downgraded |
| Adversarial citation verification | **PASS** | 223 re-fetched; 38 downgraded (caught weak identity/domain-intel cites) |
| All required categories covered | **PASS** | §3 map spans all 22 required categories across 46 providers |
| Uniform template / comparable units | **PASS** | full provider-research attribute set per provider |
| API-first exclusions applied **consistently** | **PASS** | ADR-0009 fixes the Apollo-vs-Coresignal inconsistency; verdicts preserved |
| Waterfall-placement hypothesis per provider | **PASS** | §3 seed ordering + per-entry placement feed ADR-0007 |
| Glossary terms / cross-refs resolve | **PASS** | canonical Field names used; ADR/doc links valid |
| Back-propagated + logged | **PASS** | `08` seed order, `12`/`16`/`18` provenance+continuity, `CHANGELOG` |
| Scope completeness (Gap-Analysis) | **PASS w/ 1 human item** | PR-EXCL-1 requires a human policy call (flagged, not hidden) |

**Gate recommendation:** `GATE-PASS` with **one item explicitly routed to the human** (PR-EXCL-1);
all other opens are `ACCEPTED-RISK`. **Awaiting human approval to advance to Phase 4 (System
Architecture)** — and a decision on PR-EXCL-1.

---

## 7. Implemented Adapter Ledger (200-provider rollout — ADR-0023)

This section is the running RESEARCH record for the code adapters (`internal/provider/adapters`).
Each row's **auth scheme** + **status→error-class** mapping is the VERIFIED, load-bearing contract
(exercised by the adapter tests); the request/response **field names** are taken from the cited
official docs but pinned **`UNVERIFIED`** until confirmed against a live authorized call
(`testdata/README_UNVERIFIED.md`). Verified date: **2026-07-06** unless noted. Rows are added as
each provider is implemented; EXCLUDED providers are in §6, never coded.

| Provider (slug) | Layer | Status | Endpoint (default) | Auth | Canonical Fields filled | Source |
|---|---|---|---|---|---|---|
| Hunter (`hunter`) | email-find | ACTIVE-CANDIDATE | `GET api.hunter.io/v2/email-finder` | api-key-query `api_key` | work_email, email_status | hunter.io/api-documentation/v2 |
| Prospeo (`prospeo`) | email-find | ACTIVE-CANDIDATE | `POST api.prospeo.io/email-finder` | api-key-header `X-KEY` | work_email, email_status | docs.prospeo.io |
| Twilio Lookup (`twilio-lookup`) | phone-validate | ACTIVE-CANDIDATE | `GET lookups.twilio.com/v2/PhoneNumbers/{e164}` | basic (SID:token) | phone_status | twilio.com/docs/lookup/v2-api |
| People Data Labs (`people-data-labs`) | identity | ACTIVE-CANDIDATE | `GET api.peopledatalabs.com/v5/person/enrich` | api-key-header `X-Api-Key` | work_email, mobile_phone, job_title, linkedin_url, full_name, company_name, company_domain, industry, employee_count | docs.peopledatalabs.com/docs/reference-person-enrichment-api |
| NeverBounce (`neverbounce`) | email-verify | ACTIVE-CANDIDATE | `GET api.neverbounce.com/v4/single/check` | api-key-query `key` | email_status | developers.neverbounce.com/reference/single-check |
| Kickbox (`kickbox`) | email-verify | ACTIVE-CANDIDATE | `GET api.kickbox.com/v2/verify` | api-key-query `apikey` | email_status (conf from `sendex`) | docs.kickbox.com |
| ZeroBounce (`zerobounce`) | email-verify | ACTIVE-CANDIDATE | `GET api.zerobounce.net/v2/validate` | api-key-query `api_key` | email_status, first_name, last_name | zerobounce.net/docs/email-validation-api-quickstart/v2-validate-emails |
| Apollo (`apollo`) | email-find | **DEPRIORITIZED** | `POST api.apollo.io/api/v1/people/match` | api-key-header `X-Api-Key` | work_email, email_status, linkedin_url, job_title, seniority, full_name, company_name/domain, employee_count, industry, office_phone | docs.apollo.io/reference/people-enrichment |
| Clearbit (`clearbit`) | firmographics | ACTIVE-CANDIDATE | `GET company.clearbit.com/v2/companies/find` | bearer | company_name, industry, sic/naics, employee_count, company_revenue, technographics, hq country/city, founded_year, type, company_linkedin_url | dashboard.clearbit.com/docs |
| BuiltWith (`builtwith`) | technographics | ACTIVE-CANDIDATE | `GET api.builtwith.com/v23/api.json` | api-key-query `KEY` | technographics, company_name, industry, hq country, employee_count, company_revenue | api.builtwith.com/domain-api |
| TheirStack (`theirstack`) | technographics | ACTIVE-CANDIDATE | `POST api.theirstack.com/v1/companies/technologies` | bearer | technographics (job-posting derived) | theirstack.com/en/docs/api-reference |
| G2 (`g2`) | intent | ACTIVE-CANDIDATE | `GET data.g2.com/api/v2/buyer_intent` | bearer | buying_signal, intent_topics, company_name/domain, industry, hq country, employee_count | data.g2.com/api/v2/docs |
| 6sense (`6sense`) | intent | ACTIVE-CANDIDATE | `POST scribe.6sense.com/v2/people/full` (form-urlencoded) | api-key-header `Authorization: Token <token>` | intent_score, buying_signal (buying stage), intent_topics (segments), company firmographics + naics/sic | api.6sense.com/docs |
| SalesIntel (`salesintel`) | phone-find | ACTIVE-CANDIDATE | `GET api.salesintel.io/service/people` | api-key-header `X-CB-ApiKey` | work/personal email, mobile/direct/office phone (by type), job_title/seniority/dept, LinkedIn, firmographics, naics/sic | developer.salesintel.io |
| Lusha (`lusha`) | email-find | **DEPRIORITIZED** | `POST api.lusha.com/v3/contacts/search-and-enrich` | api-key-header `api_key` | work/personal email, mobile/direct phone, job_title/seniority/dept, LinkedIn, company name/domain | docs.lusha.com |
| Kaspr (`kaspr`) | email-find | **DEPRIORITIZED** | `POST api.developers.kaspr.io/profile/linkedin` | api-key-header `Authorization` (raw) + `accept-version: v2.0` | work/personal email, mobile_phone, first/last name, LinkedIn | docs.developers.kaspr.io |
| ContactOut (`contactout`) | email-find | **DEPRIORITIZED** | `GET api.contactout.com/v1/people/linkedin?profile=` | api-key-header `token` | work/personal email, email_status (per-address map), mobile_phone, LinkedIn | api.contactout.com |
| Diffbot (`diffbot`) | firmographics | ACTIVE-CANDIDATE | `GET kg.diffbot.com/kg/v3/enhance?type=Organization&url=` | api-key-query `token` | company name/domain, employees, revenue, founded year, LinkedIn, naics/sic | docs.diffbot.com/reference/enhanceget |
| HG Insights (`hg-insights`) | technographics | ACTIVE-CANDIDATE | `POST api.hginsights.com/data-api/v2/companies/enrich` | bearer (`hg_v2_…`) | technographics (install base), company name/domain, hq country, employees, revenue | data-docs.hginsights.com/v2 |
| Vainu (`vainu`) | firmographics | ACTIVE-CANDIDATE | `GET api.vainu.io/api/v2/companies/?domain__in=` | api-key-header `API-Key` | registry firmo (name/domain/country/city, employees, revenue, industry_code, legal form, founded, LinkedIn, phone) + technographics | developers.vainu.com |
| Global Database (`global-database`) | firmographics | ACTIVE-CANDIDATE | `POST api.globaldatabase.com/v2/enrichment/url` | api-key-header `Authorization: Token <key>` | registry firmo (name/domain/phone/size/founded/industry/sic/country/city/legal form/LinkedIn) | api.globaldatabase.com/docs/v2 |
| Data Axle (`data-axle`) | firmographics | ACTIVE-CANDIDATE | `POST api.data-axle.com/v2/places/match` | api-key-header `X-AUTH-TOKEN` | company name/phone, hq city/country, LinkedIn, type, founded year (US/CA compiled) | platform.data-axle.com/places/docs |
| Owler (`owler`) | firmographics | **DEPRIORITIZED** | `GET api.owler.com/v1/companypremium/url/{domain}` | api-key-header `user_key` | name/domain, industry, employees, revenue, founded, type, hq country/city/phone, LinkedIn | developers.owler.com |
| Leadspace (`leadspace`) | firmographics | **DEPRIORITIZED** | `POST apigw.leadspace.com/enrichment/enrich/single` | bearer | name/domain, industry, employees, revenue, hq country/city, LinkedIn, phone, type, technographics | support.leadspace.com |
| NinjaPear (`ninjapear`) | firmographics | **DEPRIORITIZED** | `GET nubela.co/api/v1/company/details?website=` | bearer | name/domain, industry (GICS code), type, founded year, employees, hq country/city | nubela.co/docs |
| Dun & Bradstreet (`dnb`) | firmographics | ACTIVE-CANDIDATE | **async match→fetch**: `GET plus.dnb.com/v1/match/cleanseMatch` → `GET /v1/data/duns/{duns}` | **oauth2-cc** (`/v2/token`, Bearer) | **duns_number**, name, domain, hq country/city, employees, revenue, SIC, industry | directplus.documentation.dnb.com |
| MixRank (`mixrank`) | firmographics | **DEPRIORITIZED** | `GET api.mixrank.com/v2/json/{key}/companies/match` | **api-key-path** (key is a URL path segment) | name, domain, employees, industry, SIC, NAICS, founded year, hq country/city, type, company LinkedIn | mixrank.com/api/documentation |
| Verifalia (`verifalia`) | email-verify | ACTIVE-CANDIDATE | **async submit→poll**: `POST api.verifalia.com/v2.6/email-validations` → `GET …/{id}` | basic | email_status (classification), work_email | verifalia.com/developers |
| Dropcontact (`dropcontact`) | email-find | ACTIVE-CANDIDATE | **async submit→poll**: `POST api.dropcontact.com/v1/enrich/all` → `GET …/{request_id}` | api-key-header `X-Access-Token` | work_email, email_status, first/last/full name, company name/domain, LinkedIn, job title | developer.dropcontact.com |
| Icypeas (`icypeas`) | email-find | ACTIVE-CANDIDATE | **async submit→poll**: `POST app.icypeas.com/api/email-search` → `POST …/bulk-single-searchs/read` (token in body) | api-key-header `Authorization` (raw) | work_email, email_status (certainty), first/last/full name | api-doc.icypeas.com |
| Enrow (`enrow`) | email-find | ACTIVE-CANDIDATE | **async submit→poll**: `POST api.enrow.io/email/find/single` → `GET …?id=` | api-key-header `x-api-key` | work_email, email_status (qualification), company domain/name, first/last/full name | docs.enrow.io |
| Snov.io (`snov`) | email-find | ACTIVE-CANDIDATE | **async submit→poll**: `POST api.snov.io/v2/emails-by-domain-by-name/start` → `GET …/result?task_hash=` | **oauth2-cc** (body-form creds) | work_email, email_status (smtp_status), full_name | snov.io/api |
| SignalHire (`signalhire`) | email-find | **DEPRIORITIZED** | single-shot `POST signalhire.com/api/v1/candidate/search` (`withoutWaterfall=true`) | api-key-header `apikey` | full_name, work/personal email, mobile/office phone, job_title, company name/domain, LinkedIn | docs.signalhire.com |
| Explorium (`explorium`) | firmographics | ACTIVE-CANDIDATE | **async match→fetch**: `POST api.explorium.ai/v1/businesses/match` → `POST …/firmographics/enrich` | api-key-header `api_key` | name, domain, industry, naics, sic, employees (range), revenue (range), hq country/city, company LinkedIn | developers.explorium.ai |
| Endole (`endole`) | firmographics | ACTIVE-CANDIDATE | **async match→fetch**: `GET api.endole.co.uk/search/companies?q=` → `GET /company/{number}` | basic (`appId:appKey`) | name, SIC, founded year, hq country/city, company type (UK Companies House) | endole.co.uk/developers |
| BetterContact (`bettercontact`) | orchestration | ACTIVE-CANDIDATE | **async submit→poll**: `POST app.bettercontact.rocks/api/v2/async` → `GET …/{id}` (status=terminated) | api-key-header `X-API-Key` | work_email, email_status, first/last name, job_title (waterfall aggregator) | doc.bettercontact.rocks |
| FullEnrich (`fullenrich`) | orchestration | ACTIVE-CANDIDATE | **async submit→poll**: `POST app.fullenrich.com/api/v2/contact/enrich/bulk` → `GET …/{id}` (status=FINISHED) | bearer | work/personal email, email_status, mobile, LinkedIn, job_title, name, company name/domain/employees/industry/type/founded/hq/LinkedIn | docs.fullenrich.com |
| Wiza (`wiza`) | email-find | **DEPRIORITIZED** | **async submit→poll**: `POST wiza.co/api/individual_reveals` → `GET …/{id}` (data.status=finished) | bearer | work_email, email_status, mobile, phone_status, name, title, LinkedIn, company firmo | docs.wiza.co |
| RocketReach (`rocketreach`) | email-find | **DEPRIORITIZED** | **async submit→poll**: `GET api.rocketreach.co/api/v2/person/lookup` → `GET /person/checkStatus?ids=` (status=complete) | api-key-header `Api-Key` | work/personal email, email_status (grade), mobile, LinkedIn, job_title, company, name | docs.rocketreach.co |

**All 12 async-wave providers implemented** (submit→poll, match→fetch, oauth2 basic+body, single-shot,
path-key): none EXCLUDED — SignalHire fit as single-shot (`withoutWaterfall=true`), all others as
`AsyncHTTPAdapter`s.

| Cleanlist (`cleanlist`) | firmographics | ACTIVE-CANDIDATE | `POST api.cleanlist.ai/api/v2/enrichment/company` (company endpoint; sync) | bearer (`clapi_`) | name, domain, industry, revenue_range, employees, company LinkedIn | docs.cleanlist.ai |
| Demandbase (`demandbase`) | firmographics | ACTIVE-CANDIDATE | **async match→fetch**: `POST uapi.demandbase.com/data/b2b/v1/match` → `GET /company/{id}` | **oauth2-cc** (JSON creds, `/auth/v1/token`) | name, domain, industry, employees, revenue, hq country/city, naics, sic | developer.demandbase.com |
| PredictLeads (`predictleads`) | technographics | ACTIVE-CANDIDATE | single-shot `GET predictleads.com/api/v3/companies/{domain}` | **api-key-dual-header** (`X-Api-Key` + `X-Api-Token`) | company_name, company_domain, hq country | docs.predictleads.com |
| InfobelPRO (`infobelpro`) | firmographics | ACTIVE-CANDIDATE | single-shot `POST getdata.infobelpro.com/api/search` (`returnFirstPage=true`) | **oauth2-cc password grant** (`/api/token`) | name, domain, phone, employees, revenue, industry, founded, hq country/city, type | getdata.infobelpro.com/Help |

**Final deferred/excluded batch:** **Cognism** — deferred: real API but base host unconfirmed (two
candidates) AND every redeem value-key is inferred from `has*` flags only (below the UNVERIFIED-field
bar — a wrong host is non-functional); revisit with a live key. **Bombora** — deferred (DEPRIORITIZED):
partner-gated Basic auth + it's an async *batch Surge report* over an account list (weekly data,
derived buying-signal), an awkward single-subject fit. **InfobelPRO** — IMPLEMENTED (added
oauth2 password-grant TokenStyle; single-shot via `returnFirstPage=true`). **Cleanlist person/bulk**
endpoints deferred (stateful lead_list_id + signed quote_id).

**Wave 7 — coverage-audit gap-fill (2026-07-07):** a diff of the 200-tool spreadsheet's Tool column
(209 rows) against the registry surfaced a missed L2/L3 long-tail. Added 8 adapters — **leadmagic**
(X-API-Key), **getprospect** (`apiKey` hdr), **skrapp** (`X-Access-Key`), **tomba** (dual-header
`X-Tomba-Key`+`X-Tomba-Secret`), **cufinder** (`x-api-key`, form-encoded /tep) [L2 email-find];
**bounceban** (raw `Authorization`) [L3 verify]; **realphonevalidation** (`token` query) [L5];
**abstract-company** (`api_key` query) [L6]; **reverse-contact** (Bearer, `POST /v2/enrich/persons`
email→person, DEPRIORITIZED LinkedIn provenance, 404=free no-match) [L1 identity]. Wave-7 **EXCLUDED**: **FindThatLead** (vendor states "no
API available" — Zapier-only), **TrueMail** (defunct — domain + docs 301-redirect to GetProspect).
Wave-7 **deferred**: **Voila Norbert** (async finder whose completion is webhook-only — no documented
poll endpoint to build a pull loop against).

**Wave 8 — residual-row audit (2026-07-07):** ~15 rows dismissed in Wave 7 without cited research
were verified. Added 7: **uplead** (raw `Authorization`), **adapt-io** (dual-header `email`+`apiKey`,
200-with-APP-200-002 no-match), **aeroleads** (`api_key` query) [L2 email-find]; **scrubby**
(`x-api-key`), **enrichley** (`X-Api-Key`), **mailfloss** (Bearer) [L3 verify]; **extruct** (Bearer,
`GET /v1/companies/{domain}`) [L6 firmo]. Wave-8 **EXCLUDED** (cited): **Datanyze** (ZoomInfo-owned,
Chrome-extension only, ToS bans automation), **Persana AI** (MCP/agent, sunsetting→Rox), **Octave**
(agentic GTM — returns ICP-fit not enrichment), **Rift** (AI-SDR, no enrich API), **BookYourData**
(list-purchase, API undocumented/sales-gated), **Leadyfy** (no verifiable product — DNS fails).
Wave-8 **deferred**: **Surfe** + **Lemlist** (async enrich, poll-results path unverified — like Voila
Norbert), **Autobound** (Signal API — endpoint+auth verified but enrich response schema unconfirmed,
readme `/reference` pages 404'd).

**Rollout closeout:** every spreadsheet provider with a documented, self-serve REST API and an auth
model the egress supports is now implemented (90 adapters). The only remaining non-implemented
providers are documented EXCLUDED (scraping/no-API/OSINT/infra per ADR-0002/0009) or the two genuine
deferrals above (Cognism — unconfirmed host + fully-inferred schema; Bombora — partner-gated batch
report), both revisitable with live credentials.
| Pipl (`pipl`) | identity | **DEPRIORITIZED** | `GET api.pipl.com/search/?email=` | api-key-query `key` | full/first/last name, work/personal email, LinkedIn, mobile, job title, company name | docs.pipl.com |
| Versium (`versium`) | identity | **DEPRIORITIZED** | `GET api.versium.com/v2/contact?first=&last=` | api-key-header `x-versium-api-key` | first/last name, personal email, mobile phone (US B2B2C append) | api-documentation.versium.com |

**Wave-6 deferred/excluded (cont.):** **Endole** — deferred: UK-registry API is search→fetch
(two-step) with HTTP Basic → task #8. **Sales.Rocks** — EXCLUDED (ADR-0009): no documented
self-serve REST enrichment endpoint (platform/no-discoverable-API).

**Wave-6 (resolved via ADR-0024):** **D&B (Direct+)** — IMPLEMENTED as an async `match→fetch` +
`oauth2-cc` adapter (see ledger row `dnb`). **MixRank** — IMPLEMENTED once ADR-0024 Phase 4a added
`api-key-path` (its key is a mandatory URL **path segment**, now injected at egress). **Explorium** —
match→enrich (two-step) still pending the async wave (task #8).

**Wave-5 EXCLUDED / deferred:** **Swordfish** + **Nimbler** — EXCLUDED (ADR-0009): no documented
self-serve REST API (access is sales/account-gated; no discoverable base URL, endpoint, auth, or
response schema — implementing would be fabrication). **Deferred — async (task #8):** **Wiza**
(create→poll), **SignalHire** (POST→callback). **Deferred — mandatory account-config query param:**
**Enlyft** (every call needs a `solution_id` subscription UUID not derivable from a canonical field,
like Sinch's projectId; + unverified response envelope). **Deferred — session/multi-step auth +
unverified schema:** **Lead411** (login→JWT→reuse; response schema entirely undocumented). Still to
verify/triage: infobelpro, vainu, global-database (vainu + global-database research ran with the
safety classifier unavailable — verify citations before implementing).
| Findymail (`findymail`) | email-find | ACTIVE-CANDIDATE | `POST app.findymail.com/api/search/name` | bearer | work_email, full_name, company_domain | app.findymail.com/docs |
| Anymail Finder (`anymailfinder`) | email-find | ACTIVE-CANDIDATE | `POST api.anymailfinder.com/v5.1/find-email/person` | api-key-header `Authorization` (raw) | work_email, email_status, full_name, job_title | anymailfinder.com/email-finder-api/docs |
| Datagma (`datagma`) | email-find | ACTIVE-CANDIDATE | `GET gateway.datagma.net/api/ingress/v8/findEmail` | api-key-query `apiId` | work_email, email_status, company_domain | datagmaapi.readme.io |
| Emailable (`emailable`) | email-verify | ACTIVE-CANDIDATE | `GET api.emailable.com/v1/verify` | api-key-query `api_key` | email_status (`state`), first/last/full name | emailable.com/docs/api |
| Bouncer (`bouncer`) | email-verify | ACTIVE-CANDIDATE | `GET api.usebouncer.com/v1.1/email/verify` | api-key-header `x-api-key` | email_status (`status`) | docs.usebouncer.com |
| MillionVerifier (`millionverifier`) | email-verify | ACTIVE-CANDIDATE | `GET api.millionverifier.com/api/v3/` | api-key-query `api` | email_status (`result`) — 200-with-`error` → AUTH/QUOTA | developer.millionverifier.com |
| DeBounce (`debounce`) | email-verify | ACTIVE-CANDIDATE | `GET api.debounce.io/v1/` | api-key-query `api` | email_status (`debounce.result`) — `success:"0"`+error → AUTH/QUOTA/RATE_LIMIT | developers.debounce.com |
| Clearout (`clearout`) | email-verify | ACTIVE-CANDIDATE | `POST api.clearout.io/v2/email_verify/instant` | bearer | email_status (`data.status`) — `status:"failure"` → AUTH/QUOTA | docs.clearout.io |
| Mailgun (`mailgun-validate`) | email-verify | ACTIVE-CANDIDATE | `GET api.mailgun.net/v4/address/validate` | basic (`api:key`) | email_status (`result`) | documentation.mailgun.com/docs/validate |
| Brandfetch (`brandfetch`) | firmographics | ACTIVE-CANDIDATE | `GET api.brandfetch.io/v2/brands/{domain}` | bearer | company_name, employee_count, founded_year, industry, company_type (`kind`), hq city/country, company_linkedin_url | docs.brandfetch.com/brand-api |
| Wappalyzer (`wappalyzer`) | technographics | ACTIVE-CANDIDATE | `GET api.wappalyzer.com/v2/lookup/?urls=` | api-key-header `x-api-key` | technographics (frontend tech) | wappalyzer.com/docs/api/v2/lookup |
| Telnyx (`telnyx`) | phone-validate | ACTIVE-CANDIDATE | `GET api.telnyx.com/v2/number_lookup/{e164}?type=carrier,caller-name` | bearer | phone_status (carrier.type), mobile_phone | developers.telnyx.com/docs/identity/number-lookup |
| Vonage (`vonage`) | phone-validate | ACTIVE-CANDIDATE | `GET api.nexmo.com/ni/standard/json?number=` | basic (`key:secret`) | phone_status (network_type; status!=0 → AUTH/QUOTA/RATE_LIMIT) | developer.vonage.com/api/number-insight |
| MessageBird (`messagebird`) | phone-validate | ACTIVE-CANDIDATE | `GET rest.messagebird.com/lookup/{e164}` | api-key-header `Authorization: AccessKey <key>` | phone_status (`type`), mobile_phone | developers.messagebird.com/api/lookup |
| IPQualityScore (`ipqualityscore`) | phone-validate | ACTIVE-CANDIDATE | `GET ipqualityscore.com/api/json/phone/{phone}` | api-key-header `IPQS-KEY` | phone_status (valid+line_type; 200-`success:false` → AUTH/QUOTA), mobile_phone | ipqualityscore.com/documentation/phone-number-validation-api |
| Plivo (`plivo`) | phone-validate | ACTIVE-CANDIDATE | `GET lookup.plivo.com/v1/Number/{e164}?type=carrier` | basic (`authid:token`) | phone_status (carrier.type), mobile_phone | plivo.com/docs/lookup |
| Infobip (`infobip`) | phone-validate | ACTIVE-CANDIDATE | `POST api.infobip.com/number/1/query` | api-key-header `Authorization: App <key>` | phone_status (HLR status+error → valid/invalid/unreachable) | infobip.com/docs/number-lookup |
| NumVerify (`numverify`) | phone-validate | ACTIVE-CANDIDATE | `GET apilayer.net/api/validate?number=` | api-key-query `access_key` | phone_status (valid+line_type), mobile_phone; 200-`success:false` → classified | docs.apilayer.com/numverify |
| AbstractAPI (`abstract-phone`) | phone-validate | ACTIVE-CANDIDATE | `GET phonevalidation.abstractapi.com/v1/?phone=` | api-key-query `api_key` | phone_status (valid+`type`), mobile_phone (`format.international`) | docs.abstractapi.com/api/phone-validation |
| Veriphone (`veriphone`) | phone-validate | ACTIVE-CANDIDATE | `GET api.veriphone.io/v2/verify?phone=` | bearer | phone_status (phone_valid+phone_type), mobile_phone (`e164`) | veriphone.io/docs/v2 |
| Byteplant (`byteplant-phone`) | phone-validate | ACTIVE-CANDIDATE | `GET api.phone-validator.net/api/v2/verify?PhoneNumber=` | api-key-query `APIKey` | phone_status (status+linetype; API_KEY/RATE/DELAYED classified), mobile_phone | byteplant.com/phone-validator/api |
| Telesign (`telesign`) | phone-validate | ACTIVE-CANDIDATE | `GET rest-ww.telesign.com/v1/phoneid/{phone}` | basic (`customerid:apikey`) | phone_status (phone_type.description) | developer.telesign.com/enterprise/docs/phone-id |
| Crunchbase (`crunchbase`) | firmographics | ACTIVE-CANDIDATE | `POST api.crunchbase.com/api/v4/searches/organizations` (by website_url) | api-key-header `X-cb-user-key` | company_name/domain, LinkedIn, founded year, industry (categories), company_type, funding_stage, company_phone | data.crunchbase.com/docs |
| OpenCorporates (`opencorporates`) | firmographics | ACTIVE-CANDIDATE | `GET api.opencorporates.com/v0.4/companies/search?q=` | api-key-query `api_token` | company_name, founded year (incorporation), hq country (jurisdiction), company_type, hq city | api.opencorporates.com/documentation |
| Ocean.io (`ocean-io`) | firmographics | ACTIVE-CANDIDATE | `POST api.ocean.io/v2/enrich/company` | api-key-header `X-Api-Token` | company_name/domain, employees, industry, revenue, founded year, hq country, funding_stage, technographics | app.ocean.io/docs |
| The Companies API (`the-companies-api`) | firmographics | ACTIVE-CANDIDATE | `GET api.thecompaniesapi.com/v2/companies/{domain}` | api-key-header `Authorization: Basic <raw-token>` | company_name, industry, employees, revenue, founded year, type, hq city/country, LinkedIn, naics/sic, technographics, company_phone | thecompaniesapi.com/api |
| Coresignal (`coresignal`) | identity | **DEPRIORITIZED** | `GET api.coresignal.com/cdapi/v2/company_multi_source/enrich?website=` | api-key-header `apikey` (raw) | company_name/domain, industry, employees, founded year, hq city/country, type, LinkedIn, naics/sic, funding_stage | docs.coresignal.com |
| FullContact (`fullcontact`) | identity | **DEPRIORITIZED** | `POST api.fullcontact.com/v3/company.enrich` | bearer | company_name/domain, LinkedIn, employees, founded year, industry, hq city/country, company_phone, sic/naics | docs.fullcontact.com |
| Store Leads (`storeleads`) | firmographics | ACTIVE-CANDIDATE | `GET storeleads.app/json/api/v1/all/domain/{domain}` | bearer | company_domain/name, hq city/country, employees, revenue (cents→$), technographics (platform+apps+tech), industry | storeleads.app/api |

**Wave-4 EXCLUDED / deferred:** **TechTarget Priority Engine** — EXCLUDED (ADR-0009): no self-serve
REST enrichment endpoint; intent is delivered only via CRM connectors / SFTP / a one-way push to
6sense, no developer-facing API. **Cargo** — EXCLUDED: a GTM data-orchestration *platform* (peer to
our waterfall), not a data vendor — its REST API exposes workflow/connector management, no single
endpoint that returns canonical person/company fields. **Deferred — visitor-ID / IP-reverse-lookup
flow (not modeled):** **Albacross**, **Clearbit Reveal** (input is a visitor IPv4 — no canonical
`ip` Field in the enrichment-subject vocabulary) and **Leadfeeder/Dealfront** (an account-scoped,
date-ranged visitor *feed*, not a by-domain enrich). These are a distinct integration pattern from
by-identity enrichment; modeling them needs an IP/visitor-session input the subject model doesn't
carry. **Deferred — async/OAuth (task #8):** **Bombora** (submit→poll, CSV report), **Demandbase**
(oauth2-cc + async), **BetterContact** + **Cleanlist** (submit→poll waterfalls). **Deferred —
schema unverified:** **TrustRadius** (REST API + x-api-key auth confirmed, but the response JSON
schema is a JS-SPA doc the researcher could only infer — held until a schema is confirmable rather
than ship guessed field paths).

**Wave-3 EXCLUDED / deferred:** **UserGems** — EXCLUDED (ADR-0009): its public API is a write-only
ingestion/tracking API (POST/DELETE /v1/contact|account) that returns only a queue-confirmation
message; job-change signals are delivered via CRM sync/webhook, so there is no synchronous
request/response enrich call to back an adapter. **PredictLeads** — deferred: auth requires TWO
distinct headers (`X-Api-Key` + `X-Api-Token`), which the one-credential-per-descriptor egress
injector cannot inject (an egress-seam enhancement, tracked with task #8). **RocketReach** — deferred:
async lookup (returns `checking`→`complete`), needs the poll-capable adapter (task #8).

Notes: PDL per-value confidence is derived from the response `likelihood` (1–10). Apollo work_email
confidence is lifted to 0.90 when `email_status=="verified"`. ZeroBounce/BuiltWith return in-body
errors on HTTP 200 (bad key / out-of-credits) — their Decode returns a **classified**
`*domain.ProviderError` (AUTH/QUOTA), which the enhanced `HTTPAdapter.Fetch` now preserves so the
engine can failover the key. `technographics`/`intent_topics`/`buying_signal` are stored as a single
sorted, comma-joined value (`adapters.normList`, ADR-0023).

### Deferred — multi-step / async providers (need a poll/redeem-capable adapter)
The single-shot `HTTPAdapter` (one Build+Decode) cannot express a two-call flow, so these Wave-0
providers are researched (docs/03 specs captured) but **not yet coded**, pending an async-adapter
enhancement: **Dropcontact** (`POST /batch` → poll `GET /batch/{id}`), **Cognism** (Enrich preview →
Redeem by `redeemId`, 1 credit), **FullEnrich** (submit → webhook/poll), **Icypeas** + **Enrow**
(submit → poll), **Snov.io** (OAuth2 client-credentials token exchange → call, also async). Tracked
as an enhancement item; no fabricated single-call adapter is shipped for them.
(NOTE: the ADR-0024 async foundation later landed, so every provider named above is now implemented.)

### Wave 9 — additional providers beyond the 200-tool sheet (net-new, 2026-07-07)
Thirteen further real-API providers researched (each fact cited or UNVERIFIED per `provider-research`)
and implemented, expanding coverage past the reconciled 200-tool spreadsheet. Each adapter is
secret-free (`AuthDescriptor` only), maps solely canonical `domain.Field`s, and keeps wire-shapes
UNVERIFIED until a live key. All 13 use existing auth schemes (api-key-query/header, bearer, basic)
already accepted by the `providers.auth_scheme` CHECK (migrations 0005+0013) — no new migration.

**Email verify (single-shot):**
- **QuickEmailVerification** — `GET /v1/verify?email=` (apikey query) → `result` (valid|invalid|unknown)
  + `domain`. 200-with-error `success:"false"` (string booleans). [docs.quickemailverification.com]
- **MyEmailVerifier** — `GET /api/validate_single.php?email=` (apikey query) → `Status`
  (Valid|Invalid|Catch-all|Unknown). JSON error `{status:false,error:CODE}`. [github.com/pat-myemailverifier/myemailverifier-api]
- **MailboxValidator** — `GET /v2/validation/single?email=` (key query) → `status` (bool) + `email_address`
  + `domain`; nested `error.error_code` table (10004 credits→QUOTA at HTTP 401). [mailboxvalidator.com/api-single-validation]
- **Bouncify** — `GET /v1/verify?email=` (apikey query) → `result` (deliverable|undeliverable|unknown|accept_all). [bouncify.readme.io]
- **EmailListVerify** — `GET /api/verifyEmailDetailed?email=` (x-api-key header) → `result` enum +
  `firstName`/`lastName`. (Sibling `/api/verifyEmail` returns bare plain text; we use the JSON *Detailed*
  endpoint.) 400=rate-limit / 403=credit are documented discrepancies vs the shared status map. [api.emaillistverify.com/api-doc]

**Phone validate (single-shot):**
- **Trestle** — `GET /3.0/phone_intel?phone=` (x-api-key) → `is_valid`+`line_type` → normalized phone_status;
  200-with-error `error{name,message}`→TRANSIENT, `warnings[]`→no verdict. [docs.trestleiq.com]
- **NumLookupAPI** — `GET /v1/validate/{number}` (apikey header; number is a PATH segment) →
  `valid`+`line_type`+`international_format`; invalid = HTTP 200 `valid:false`. [numlookupapi.com/docs/validate]

**Firmographics:**
- **CompanyEnrich** — `GET /companies/enrich?domain=` (Bearer) → name/domain/type/industry, BUCKETED
  `employees`+`revenue` (~0.65), `financial.funding_stage`, `founded_year`, `location.{country,city,phone}`,
  `socials.linkedin_url`, `naics_codes[]`, `technologies[]`. 404 = no-match. [docs.companyenrich.com]
- **Companies House (UK — official, free)** — match→fetch: `GET /search/companies?q=` → `items[0].company_number`,
  then `GET /company/{n}` → company_name/type/date_of_creation/`sic_codes[]` (UK SIC 2007, not NAICS)/
  registered_office_address.{country,locality}. Basic auth = **API key as username, empty password**
  (pool secret `"<KEY>:"`). GB-only registry. [developer.company-information.service.gov.uk]

**Identity (DEPRIORITIZED — LinkedIn/public-web provenance, ADR-0009):**
- **Enrich.so** — `POST /api/v3/reverse-lookup/lookup` `{email}` (x-api-key; current v3 host is literally
  `dev.enrich.so`, verified live) → `data.{displayName,firstName,lastName,profileUrl,companyName,
  positions.positionHistory[0].title}`. 402=insufficient credits; not charged on no-match. [doc.enrich.so]

**Revisited async deferrals — now implemented (poll path confirmed):**
- **Surfe** — submit `POST /v2/people/enrich` → `enrichmentID`; poll `GET /v2/people/enrich/{id}` until
  `status ∈ {COMPLETED,FAILED}` (IN_PROGRESS while running). Bearer. Maps emails[].validationStatus,
  mobilePhones[], jobTitle, seniorities/departments (normalized), linkedInUrl. 403 = quota OR credits. [developers.surfe.com]
- **Lemlist** — submit `POST /enrich?findEmail=true&verifyEmail=true&…` → `{id}`; poll `GET /enrich/{id}`
  until `enrichmentStatus="done"` (202 while in progress). Basic auth = **empty username + key as password**
  (pool secret `":<KEY>"`). The GET poll body yields `data.email.{email,notFound}` → work_email + email_status;
  the richer LinkedIn-enrichment fields are **webhook-only** and intentionally not mapped. [developer.lemlist.com]
- **Voila Norbert** — `POST /search/name` (form fields name,domain; Basic auth = **any username : token**,
  pool secret `"any:<TOKEN>"`) → `email.{email,is_done,score}`. Single-shot happy path (is_done=true);
  no documented GET/poll endpoint (async result is webhook-only), so a pending lookup yields no value here. [voilanorbert.com/api]

### Wave 10 — further net-new providers (2026-07-08)
Twelve more researched with cited official docs; **10 implemented**, 1 deferred, 1 excluded.

**Email verify (single-shot):**
- **Cloudmersive** — `POST /validate/email/address/full` ("Apikey" header); request body is a BARE JSON
  string (the quoted email). `ValidAddress` bool → valid|invalid; PascalCase/underscore keys. [api.cloudmersive.com/docs + official client source]
- **Abstract Email Validation** — `GET https://emailvalidation.abstractapi.com/v1/?api_key=&email=` →
  `deliverability` (DELIVERABLE|UNDELIVERABLE|UNKNOWN). Boolean sub-checks are nested {value,text}
  objects. 422 = out of credits. [docs.abstractapi.com/api/email-validation]
- **MailerCheck** — `POST /api/check/single` (Bearer) → minimal `{status}` body (valid|catch_all|…|blocked);
  60 req/min. [developers.mailercheck.com/email]
- **Reoon** — `GET /api/v1/verify?email=&mode=power` (key query) → status (safe|invalid|…|unknown);
  200-with-error `{"status":"error","reason"}`. [reoon.com API doc]
- **Mails.so** — `GET /v1/validate?email=` (x-mails-api-key header) → `{data,error}` envelope,
  `data.result` (deliverable|undeliverable|risky|unknown). [docs.mails.so]
- **Email Hippo MORE** — `GET /v3/more/json/{key}/{email}` — the license key is a URL PATH segment
  (AuthAPIKeyPath, second consumer after MixRank). `mailboxVerification.result` (Ok|Bad|RetryLater|
  Unverifiable, decoded defensively string-or-int); 401 conflates bad key AND quota. [email-verify-api-docs.readthedocs.io]
- **Truelist** — `POST /api/v1/verify_inline?email=` (Bearer; the address is a QUERY param on a POST) →
  `emails[0].email_state` (ok|email_invalid|risky|accept_all|unknown). [truelist.io/docs/api]

**Phone validate (single-shot):**
- **NeutrinoAPI** — `POST /phone-validate` (form); DUAL headers **User-ID + API-Key** (pool secret
  `"<user-id>:<api-key>"`). KEBAB-CASE response keys; `valid`+`type` → phone_status,
  `international-number` → mobile_phone. Number-plan validation, not live HLR. [neutrinoapi.com/api/phone-validate]

**Firmographics (single-shot):**
- **BigPicture.io** — `GET /v1/companies/find?domain=`; the RAW key is the whole Authorization header
  value (no Bearer prefix). Clearbit-style body: category.{industry,naicsCode}, metrics.{employees,
  annualRevenue}, geo.*, foundedYear; `linkedin.handle` is a bare handle → adapter prefixes
  https://www.linkedin.com/. 202 = lookup queued (re-request later; no job token). [docs.bigpicture.io/api]

**Identity (DEPRIORITIZED — US public-records/people-search provenance, ADR-0009):**
- **Enformion (EnformionGO)** — `POST /Contact/Enrich`; DUAL headers **galaxy-ap-name +
  galaxy-ap-password** (pool secret `"<name>:<password>"`) + static routing header
  `galaxy-search-type: DevAPIContactEnrich` set by Build. ≥2 of name/phone/address/email required.
  200-with-`isError` body; charge-on-match. Maps person.name, top phone (+isConnected→phone_status),
  first non-business email → personal_email. [enformiongo.readme.io]

**Deferred:**
- **Sinch Number Lookup v2** — clean oauth2-cc API (token at auth.sinch.com, Basic style), but the
  endpoint is `POST /v2/projects/{projectId}/lookups`: the account-specific `projectId` is a mandatory
  URL path segment that is neither a secret (not injectable via key pool) nor derivable from the
  subject — the same mandatory-config blocker as Enlyft's solution_id. Deferred until per-provider
  config plumbing exists. [developers.sinch.com/docs/number-lookup-api-v2]

**EXCLUDED (§6):**
- **FindThatLead** — the vendor's own help center states verbatim "Sorry, we don't have an API
  available" (Zapier/Make only). The `api.findthatlead.com/v1` base echoed by search summaries is
  unconfirmed by any official source and was NOT adopted.
  [helpdesk.findthatlead.com/en/article/do-we-have-an-api-1hlliac/]

### Wave 11 — official registries + aggregators (2026-07-08)
The three no-auth open-data registries (brreg, gleif, recherche-entreprises — AuthNone, first
VERIFIED-live wire shapes) are described in the CHANGELOG entries; further Wave-11 verdicts:
- **north-data** (DEPRIORITIZED, implemented) — official European register data via a clean OpenAPI
  Data API (`GET /company/v1/company`, X-Api-Key); deprioritized solely for heavy onboarding: keys
  are issued manually via support email, €500/month minimum, 12-month contract.
  [github.com/northdata/api user guide + swagger.yaml]
- **opensanctions** (DEPRIORITIZED, implemented) — yente match API (`POST /match/default`,
  "Authorization: ApiKey <key>"); sanctions/PEP/watchlist screening only — returns data solely for
  risk-listed entities, so it is an optional compliance screen, not a firmographics waterfall source.
  Adapter accepts values only when the API asserts match==true, scaling confidence by the returned
  score. [opensanctions.org/docs/api + api.opensanctions.org/openapi.json]
- **EXCLUDED: abn-lookup (Australian Business Register)** — the JSON web service at
  abr.business.gov.au/json is JSONP-ONLY: verified live that omitting the callback param still
  returns `callback({...})` with Content-Type text/javascript; the only other official interface is
  SOAP/XML (abrxmlsearch.asmx). JSONP/SOAP-only matches the exclusion criteria. The wrapper is a
  fixed `callback(`/`)` affix, so the source is recoverable if a wrapper-stripping adapter is ever
  sanctioned. [abr.business.gov.au, live-verified 2026-07-08]
