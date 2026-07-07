# Implementation Progress

**Last updated:** 2026-07-06 (provider adapter rollout started) · Legend: `DRAFT` / `IN-REVIEW` / `APPROVED` / `BLOCKED` · gate = human approval required.

## Phase status
| Phase | Scope | Doc(s) | Status | Gate |
|-------|-------|--------|--------|------|
| 0 | Bootstrap tooling | `00b` + `/skills` `/agents` `/commands` `/adr` | ✅ **complete** | — (not a planning gate) |
| 1 | Market research | `01` | ✅ **APPROVED** (18 competitors, cited + verified) | ✅ passed |
| 2 | Waterfall research | `02` | ✅ **APPROVED** (5 tracks cited+verified; ADR-0004…0008) | ✅ passed |
| 3 | Provider research + matrix | `03` | ✅ **APPROVED** (46-provider roster; ADR-0009; PR-EXCL-1 resolved) | ✅ passed |
| 4 | System architecture | `04` | ✅ **APPROVED** (modulith+data-plane; ADR-0010; 3 diagrams) | ✅ passed |
| 5 | Microservices | `05` | ✅ **IN-REVIEW** (module catalog; `dependencies.mmd`) | auto-advance ✅ recorded |
| 6 | Database + ERD | `06` | ✅ **IN-REVIEW** (ADR-0011 RLS-pool; `erd.mmd`) | auto-advance ✅ recorded |
| 7 | API gateway | `07` | ✅ **IN-REVIEW** (ADR-0012 REST+webhooks) | auto-advance ✅ recorded |
| 8 | Waterfall orchestrator | `08` | ✅ **IN-REVIEW** (routing/plan; ADR-0007/0008) | auto-advance ✅ recorded |
| 9 | Execution engine | `09` | ✅ **IN-REVIEW** (gates spine; ADR-0005/0006) | auto-advance ✅ recorded |
| 10 | Queue system | `10` | ✅ **IN-REVIEW** (ADR-0013 Redpanda + ADR-0014 Temporal-gated; 2 diagrams) | auto-advance ✅ recorded (QS-TMP-1 flagged) |
| 11 | Scaling strategy | `11` | ✅ **IN-REVIEW** (Little's-Law sizing) | auto-advance ✅ recorded |
| 12 | Provider key management | `12` | ✅ **IN-REVIEW** (key pools; continuity+correlation) | auto-advance ✅ recorded |
| 13 | Proxy / egress management | `13` | ✅ **IN-REVIEW** (SSRF-safe egress) | auto-advance ✅ recorded |
| 14 | Intent engine | `14` | ✅ **IN-REVIEW** (providers cited `03`) | auto-advance ✅ recorded |
| 15 | Verification engine | `15` | ✅ **IN-REVIEW** (providers cited `03`) | auto-advance ✅ recorded |
| 16 | Cost optimization | `16` | ✅ **IN-REVIEW** (ceilings + charge-on-success) | auto-advance ✅ recorded |
| 17 | Dashboard planning | `17` | ✅ **IN-REVIEW** (panels→data mapping; RBAC) | auto-advance ✅ recorded |
| 18 | Security (SSRF + tenant isolation) | `18` | ✅ **IN-REVIEW** (2-layer G1 + SSRF + STRIDE + compliance/DR) | auto-advance ✅ recorded |
| 19 | Deployment | `19` | ✅ **IN-REVIEW** (ADR-0015; cells; 2 diagrams) | auto-advance ✅ recorded |
| 20 | Monitoring | `20` | ✅ **IN-REVIEW** (golden signals + waterfall KPIs + SLOs) | auto-advance ✅ recorded |
| 21 | Testing | `21` | ✅ **IN-REVIEW** (negative gate tests + load = UNVERIFIED→VERIFIED) | auto-advance ✅ recorded |
| 22 | Future roadmap | `22` | ✅ **IN-REVIEW** (backlog captured) | auto-advance ✅ recorded |
| — | **Planning Completion Gate** | all | ✅ **PASS** — 5 reviewers; **5 blocking FAILs fixed**, WARNs addressed; see ledger §PCG | **awaiting human approval** |
| — | Implementation | code | ⛔ not allowed until human approves the Planning Completion Gate | — |

## Provider adapter rollout (200-tool architecture — ADR-0023, started 2026-07-06)
Turning `Closo_Enrichment_Architecture_200_Tools` into researched, tested, wired adapters. Bridge
(Phase A) complete: Field vocabulary 18→33 (doc-first, `docs/00 §7`), append-only adapter registry
(`internal/provider/adapters/registry.go`), code→catalog seeder (`cmd/providerseed` +
`providers.Seed`), binaries wired to `adapters.All(egress)`. Per-provider discipline: docs-cited
research (§7 ledger `docs/03`), secret-free adapter on the `hunter.go` pattern, `<slug>_found.json`
fixture, table-driven decode test, registry entry; wire-shapes `UNVERIFIED` until a live key. Build
stays green after every provider.

| Wave | Scope | Implemented | Status |
|------|-------|-------------|--------|
| — | pre-existing | hunter, prospeo, twilio-lookup | ✅ (fixtures UNVERIFIED) |
| 0 | Recommended Starting Stack | people-data-labs, apollo, zerobounce, clearbit, builtwith, theirstack, g2 | ✅ synchronous set done |
| 1 | L2 email-find | findymail, anymailfinder, datagma (+ hunter/prospeo/apollo) | ✅ (icypeas/enrow/snov deferred-async) |
| 1 | L3 email-verify | neverbounce, kickbox, zerobounce, emailable, bouncer, millionverifier, debounce, clearout, mailgun-validate | ✅ (verifalia deferred-async) |
| — | deferred (multi-step/async) | dropcontact, cognism, fullenrich, icypeas, enrow, snov, verifalia | ⏸ researched; need async/redeem/oauth adapter enhancement (docs/03 §7) |

**74 adapters live** across L1(5)/L2(16)/L3(10)/L4(1)/L5(12)/L6(21)/L7(5)/L8(2)/L9(2). **ADR-0024
is fully implemented (all phases 1–4)**: per-adapter CallPolicy, oauth2-cc (basic/body/json/password
token styles + access_token/accessToken parsing), AsyncHTTPAdapter (submit→poll + match→fetch),
api-key-path, and api-key-dual-header.

**90 adapters** by category: email-find 24, firmographics 23, email-verify 14, phone-validate 13,
identity 6, technographics 5, orchestration 2, intent 2, phone-find 1. **Wave 8 (task #14) COMPLETE**
— verified the ~15 rows Wave 7 dismissed without cited research; added uplead/adapt-io/aeroleads (L2),
scrubby/enrichley/mailfloss (L3), extruct (L6). EXCLUDED (cited): Datanyze, Persana AI, Octave, Rift,
BookYourData, Leadyfy. Deferred: Surfe, Lemlist, Autobound (async/unverified schema). Every
spreadsheet row now has a cited verdict. **Wave 7 (task #13) COMPLETE**
— coverage audit of the 200-tool spreadsheet (2026-07-07) caught a missed L2/L3 long-tail; added
leadmagic/getprospect/skrapp/tomba/cufinder (L2), bounceban (L3), realphonevalidation (L5),
abstract-company (L6), reverse-contact (L1). Wave-7 EXCLUDED: FindThatLead (no API), TrueMail
(defunct). Deferred: Voila Norbert (webhook-only async). Remaining non-implemented spreadsheet
rows are EXCLUDED (docs/03 §6: scraping/no-API/OSINT/infra, visitor-ID, enterprise-gated, no-code) or
live-key-gated deferrals (Cognism, Bombora). Providers on it: D&B/Explorium/Endole/Demandbase (match→fetch), Snov (oauth2
body), Cleanlist (company sync), MixRank (path-key), PredictLeads (dual-header), SignalHire
(single-shot), verifalia/dropcontact/icypeas/enrow/wiza/rocketreach (submit→poll), bettercontact/
fullenrich (L9 waterfall). `go build ./...` +
`go test ./...` green. **Waves 0–6 COMPLETE.** Wave 6 (11 researched) added data-axle, owler,
leadspace, ninjapear (L6) + pipl, versium (L1). Wave-6 EXCLUDED: Sales.Rocks (no self-serve API).
Deferred: D&B (oauth2-cc + match→fetch), Explorium (match→enrich), Endole (search→fetch + Basic) →
task #8; MixRank (path-segment API key, incompatible with header/query egress injector).

**The cleanly-implementable synchronous single-shot provider set across L1–L8 is now complete
(56 adapters).** All further implementable providers are blocked on **task #8** — the
async/multi-step + multi-credential + non-header-auth egress enhancement. **ADR-0024** now records
the full design (phased). **Phases 1–3 DONE:** (1) per-adapter `CallPolicy`
(`provider.PolicyOverrider` + `HTTPAdapter.Policy` + engine `policyFor` — a slow provider declares a
longer *bounded* budget, G3 preserved); (2) `oauth2-cc` token exchange + caching in `AuthInjector`
(`AuthDescriptor.TokenURL`); (3) `provider.AsyncHTTPAdapter` (submit→poll + match→fetch, ctx-bounded
poll loop, egress auth per round-trip). All tested + green. **First async provider WIRED: D&B
Direct+** (`dnb`, 57th adapter) — match→fetch + oauth2-cc + bounded budget, all three phases proven
end-to-end (`TestDNB_MatchFetchOAuth2`/`_NoMatch`). The registry now carries `New` **or** `NewAsync`
and routes through `Registered.Construct` → `provider.Introspectable` (both adapter kinds satisfy it),
so async adapters seed + wire identically. NEXT async consumers: a submit→poll verifier
(Verifalia/Dropcontact/Icypeas/Enrow/Snov), Explorium/Endole (match→fetch). Remaining infra phase:
4 (path-segment key for MixRank, two-header auth for PredictLeads — deferred niche). Deferred
provider set:
dropcontact, cognism, fullenrich,
cleanlist, bettercontact, bombora, demandbase, icypeas, enrow, snov, verifalia, rocketreach, wiza,
signalhire, infobelpro, predictleads, D&B, Explorium, Endole (async/multi-step); MixRank
(path-segment key). Plus a thin not-yet-researched tail (a few L8/L9 platforms, mostly EXCLUDED
orchestration/no-code per ADR-0009). Wave 5 (15 researched) added SalesIntel (L4),
Lusha/Kaspr/ContactOut (L2 DEPRIORITIZED), Diffbot/Vainu/GlobalDatabase (L6), HG Insights (L7).
Wave-5 EXCLUDED: Nimbler, Swordfish. Deferred: Lead411 (JWT session), Wiza/SignalHire/InfobelPro
(async), Enlyft (solution_id config).

**Rollout status:** the cleanly-implementable **synchronous by-identity** providers across L1–L8 are
now done (50 adapters). Remaining implementable work is:
- **Task #8 — async / multi-credential egress enhancement** (~16 providers blocked): dropcontact,
  cognism, fullenrich, cleanlist, bettercontact, bombora, demandbase, icypeas, enrow, snov, verifalia,
  rocketreach, wiza, signalhire, infobelpro, predictleads. Needs the engine per-call-timeout +
  multi-cred-egress design decision (touches gate G3) — a deliberate plan pass, not a mid-loop edit.
- **Long tail, sync, not yet researched:** L1 (Pipl, Versium, Data Axle, Explorium, MixRank, Owler),
  L6 (D&B, Leadspace, NinjaPear, Sales.Rocks, Endole), a few L8/L9.
- **EXCLUDED (docs/03 §6):** defunct/no-API/scraping/infra/no-code + visitor-ID/IP-flow providers. Helpers: `classifyErrMsg`, `phoneStatusFromType`, `yearOf`, `bareDomain`.

**What remains, by category:**
- **Async / multi-credential (task #8 — needs the engine per-call-timeout + multi-cred-egress design
  decision):** dropcontact, cognism (enrich→redeem), fullenrich, icypeas, enrow, snov (oauth2-cc),
  verifalia, bettercontact, cleanlist, bombora (submit→poll CSV), demandbase, rocketreach,
  predictleads (two headers). ~13 providers.
- **EXCLUDED (documented in docs/03 §6, ADR-0002/0009):** Proxycurl (defunct), LinkedIn Sales Nav
  (no API), UserGems + TechTarget + Cargo (no field-returning REST enrich), OSS OSINT CLIs
  (theHarvester/Sherlock/…), infra/libraries (libphonenumber, Reacher, Redis, Postgres, PSL), no-code
  scrapers (Zapier/n8n/PhantomBuster/Bardeen/Apify/Bright Data).
- **Deferred — visitor-ID/IP flow not modeled:** Albacross, Clearbit Reveal, Leadfeeder (input is a
  visitor IP / a feed, not a canonical enrichment key).
- **Long tail still to research/implement (sync, by-identity):** L4 phone-find (Swordfish, Nimbler,
  Nymblr, SalesIntel, Lead411), DEPRIORITIZED contact finders (Lusha, Kaspr, ContactOut, Wiza,
  SignalHire, LeadIQ, Seamless, Surfe, Evaboot, Reverse Contact), more L6 firmo (D&B, Diffbot,
  InfobelPRO, Vainu, Global Database, Leadspace, NinjaPear), L7 (HG Insights, Enlyft), L1 (Pipl,
  Versium, Data Axle, Explorium, MixRank, Owler).
**L5 phone-validation complete (12):** twilio-lookup, telnyx, vonage, messagebird, ipqualityscore,
plivo, infobip, numverify, abstract-phone, veriphone, byteplant-phone, telesign — all normalized to
one `phone_status` vocabulary via `phoneStatusFromType`. Engine helpers: `classifyErrMsg`
(200-with-error → AUTH/QUOTA/RATE_LIMIT), `423 → QUOTA`, `phoneStatusFromType`. **Deferred**: sinch
(projectId path config), diffbot (KG schema), async/OAuth multi-step set (task #8). Next: **L4
phone-find** (Swordfish, Nimbler, Nymblr, SalesIntel, Lead411 — mostly DEPRIORITIZED), then **L8
intent** (Bombora, 6sense, Demandbase, TechTarget, TrustRadius, UserGems…), **L9 orchestration**
(BetterContact, Cleanlist, Cargo), and L1/L6 remainder (Coresignal, D&B, Crunchbase, OpenCorporates,
Ocean.io, PredictLeads…).
**Task #8 (async-adapter enhancement)** intentionally deferred: it changes the engine G3 timeout
policy, so it needs a deliberate plan pass (not an autonomous rush) before dropcontact/cognism/
fullenrich/icypeas/enrow/snov/verifalia can be coded.

## PCG — Planning Completion Gate ledger (2026-07-01)
Adversarial review `wf_15689f67-653`: 5 reviewers (gap-analysis, correctness-gates, security, consistency,
cost-scale), 32 findings (5 FAIL/blocking, 17 WARN, 10 INFO).

**Blocking FAILs — all FIXED:**
| # | Finding | Fix |
|---|---------|-----|
| B1 | `api-integration` adapter received the provider secret — contradicts keys-injected-at-egress | skill now: adapter emits an **auth descriptor** + key-pool selector; egress-proxy injects the secret |
| B2 | Idempotency-key derivation inconsistent (3 variants; skill ≠ impl docs) | canonical key set in `waterfall-correctness` G2 + stored on `field_versions` (erd); matches `04/09/10` |
| B3 | Analytics store (ClickHouse) claimed datastore-enforced isolation but has no RLS | added ClickHouse **row-policy + mandatory server-side tenant predicate + CI test** (`04/06/18`) |
| B4 | Outbound webhook allow-list was global → cross-tenant PII egress path | webhook target must be in the **delivering tenant's** registered set (`13/18`) |
| B5 | ADR index missing ADR-0015 + stale "cloud deferred" footer | index + footer corrected |

**WARNs addressed:** intent lane now asserts G3 + egress (`14`); outbox boundaries enumerated + CDC relay
(`10`); SSRF IP-encoding-bypass closed (`13`); audit immutability mechanized (hash-chain + WORM, `18`);
Little's-Law input harmonized to 350 ms across `00/04/11`; "account" (ABM) glossary note (`00`); SSOT
diagram map corrected (`doc-consistency`); duplicate tracker row removed; status snapshots refreshed.

**Accepted gaps (documented, non-blocking, safe fallback):**
| ID | Item | Disposition |
|----|------|-------------|
| AG-GRAPHQL | GraphQL named in scope but deferred (ADR-0012) | accepted; roadmap `22` with revisit trigger |
| AG-GRPC | gRPC internal has no day-1 consumer (modulith) | reserved, no day-1 consumer (honest) |
| QS-TMP-1 | Temporal cost-gate (ADR-0014) | human chose "decide later"; fallback = hand-rolled saga (now fully specified) |
| AG-ARTIFACTS | OpenAPI spec, full DDL, STRIDE detail, autoscale thresholds | first implementation tasks (contract tests already release-blockers, `21`) |
| AG-SECRETS | Vault vs cloud-KMS backend | ADR at implementation start (KM-1) |
| AG-UNVERIFIED | throughput, per-provider latency/cost, fleet sizing | `UNVERIFIED` → load test (`21`) turns them VERIFIED |

## Required diagrams (kept in sync every phase)
| Diagram | File | Status |
|---------|------|--------|
| Architecture | `diagrams/architecture.mmd` | ✅ Phase 4 (real component graph) |
| Enrichment pipeline (methodology) | `diagrams/enrichment-pipeline.mmd` | ✅ Phase 2 |
| Dependencies | `diagrams/dependencies.mmd` | ✅ Phase 5 |
| API flow | `diagrams/api-flow.mmd` | ✅ Phase 4 |
| Database ERD | `diagrams/erd.mmd` | ✅ Phase 6 |
| Deployment | `diagrams/deployment.mmd` | ✅ Phase 19 |
| Infrastructure | `diagrams/infrastructure.mmd` | ✅ Phase 19 |
| Event flow | `diagrams/event-flow.mmd` | ✅ Phase 4 |
| Queue flow | `diagrams/queue-flow.mmd` | ✅ Phase 10 |
| Retry flow | `diagrams/retry-flow.mmd` | ✅ Phase 10 |

## Module implementation checklist (populated after Planning Completion Gate)
> No module may be implemented until its plan is `APPROVED` and the Planning Completion Gate passes.

### Slice 01 — correctness-gate vertical slice (Go 1.26) — `IMPLEMENTED` 2026-07-01 (`docs/23`)
`go build/vet/test/gofmt` all clean; runnable via `go run ./cmd/enrichd`.
| Module (`internal/`) | Plan | Code | G1–G5 | Test | Done |
|--------|------|------|-------|------|------|
| `domain` (fields, provenance, idem-key, error taxonomy) | `05`/skills | ✅ | G2 key, G5 validity | via store/engine | ✅ |
| `tenant` (principal binding) | `04`/`18` | ✅ | **G1** | `context_test` | ✅ |
| `provider` (adapter, breaker, bounded Call, HTTPAdapter) | `05`/`09`/`13` | ✅ | **G3** | `call_test`,`httpadapter_test` | ✅ |
| `router` (reservation-value Plan) | `08` | ✅ | proposes only | `plan_test` | ✅ |
| `engine` (spine, fusion, SPRT-lite) | `09` | ✅ | **G2/G3/G4/G5** | `engine_test` | ✅ |
| `store` (idempotency/cost/field_versions, memory) | `06` | ✅ | **G1/G2/G4/G5** | `memory_test` | ✅ |
| `migrations/0001_init.sql` (FORCE RLS DDL) | `06`/`18` | ✅ (DDL) | G1 datastore | integration (CI) ⏳ | authored |

### Slice 02 — API gateway + async job queue (Go) — `IMPLEMENTED` 2026-07-01 (`docs/24`)
`go build/vet/test/gofmt` clean; **live HTTP smoke passed**; runnable via `go run ./cmd/enrichapi`.
| Module (`internal/`) | Code | Gates preserved | Test | Done |
|--------|------|-----------------|------|------|
| `job` (Job, tenant-scoped Store, bounded priority Queue, worker Dispatcher) | ✅ | G1 (job scope + worker principal) | `queue_test`,`dispatcher_test` | ✅ |
| `api` (Authenticator, RateLimiter, Server, handlers, DTO/validation) | ✅ | **G1** (principal-only tenant), G2 (Idempotency-Key) | `server_test` (12) | ✅ |
| `cmd/enrichapi` | ✅ | wiring | live smoke | ✅ |

### Slice 03 — durable queue + transactional outbox (Go) — `IMPLEMENTED` 2026-07-01 (`docs/25`)
`go build/vet/test/gofmt` clean; **live restart-recovery verified** (job state survived a full process kill+restart via the on-disk WAL).
| Module (`internal/`) | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `durable/log` (framed WAL, CRC, atomic batches, torn-tail recovery) | ✅ | crash-safe append/replay | `log_test` (3) | ✅ |
| `durable/store` (durable JobStore + transactional outbox) | ✅ | G1 + atomic job+intent | `recovery_test` | ✅ |
| `durable/relay` (outbox→queue, at-least-once) | ✅ | re-drives non-terminal on recovery | `recovery_test` | ✅ |
| `job/submitter` (Submitter seam: in-proc vs durable) | ✅ | API-agnostic delivery | via `api`/`durable` | ✅ |
| `cmd/enrichapi` (`DURABLE_LOG` selects durable) | ✅ | crash-safe wiring | live restart | ✅ |

**Crux:** at-least-once redelivery is safe because engine G2 makes the re-run's provider call free of a second charge (proven by `TestPipeline_CrashRecoveryExactlyOnceCharge`).

### Slice 04 — real provider adapters + egress key-injection seam (Go) — `IMPLEMENTED` 2026-07-01 (`docs/26`)
`go build/vet/test/gofmt` clean. Adapters are **secret-free** (key injected at egress, tested).
| Module (`internal/`) | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `provider/egress` (KeyResolver + AuthInjector RoundTripper) | ✅ | secret injected at egress, not in adapter | `httpadapter_test`,`adapters_test` | ✅ |
| `provider/adapters/hunter` (query api_key; 403→RATE_LIMIT) | ✅ | work_email+email_status | `adapters_test` | ✅ |
| `provider/adapters/prospeo` (X-KEY header; 402→QUOTA) | ✅ | work_email+email_status | `adapters_test` | ✅ |
| `provider/adapters/twilio` (HTTP Basic; 404→NOT_FOUND) | ✅ | phone_status | `adapters_test` | ✅ |
| Field vocab +`first_name`/`last_name`/`full_name` (`00` §7) | ✅ | email-finder match keys | — | ✅ |

**Note:** vendor wire formats are `UNVERIFIED`/representative (confirm vs live API before prod; risk localized to Build/Decode). Real adapters proven through Router+Engine with G5 provenance (`TestAdapters_EngineIntegration`).

### Slice 05 — egress-proxy / SSRF choke (Go) — `IMPLEMENTED` 2026-07-01 (`docs/27`)
`go build/vet/test/gofmt` clean. The **P2 security risk** now has concrete, tested enforcement.
| Module (`internal/provider`) | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `ssrf.go` — `HostAllowList`, dial-time IP guard, `hostGuard`, `NewEgressClient` | ✅ | HTTPS-only + allow-list + resolved-IP guard (rebinding/encoding safe) + redirect re-check + key injection at choke | `ssrf_test` (4) | ✅ |

**Corpus:** 17 internal addresses (metadata/RFC1918/loopback/ULA/link-local/CGNAT/0.0.0.0-8/IPv4-mapped) all blocked; real loopback dial blocked at the guard. `ErrSSRFBlocked` → non-retryable BAD_REQUEST. **Note:** network-level default-deny egress is still required (belt-and-suspenders, `docs/27` §4).

> **Both top-2 risks now enforced in code + tested:** G1 tenant isolation (Slices 01–03) · P2 SSRF (Slice 05).

### Slice 06 — webhooks-out (tenant-bound) + OpenAPI (Go) — `IMPLEMENTED` 2026-07-01 (`docs/28`)
`go build/vet/test/gofmt` clean.
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `job` OnComplete hook | ✅ | fires after durable-terminal; decouples webhook | via `webhook` | ✅ |
| `internal/webhook` (registry, HMAC sign/verify, tenant-bound egress delivery, bounded retries) | ✅ | **G1** tenant-bound (no cross-tenant egress) + SSRF-safe + signed | `webhook_test` (6) | ✅ |
| `docs/api/openapi.json` + `api/openapi_test.go` (contract test) | ✅ | spec↔impl bound (declared status codes) | `openapi_test` (2) | ✅ |

### Slice 07 — observability (metrics + structured logs) (Go) — `IMPLEMENTED` 2026-07-01 (`docs/29`)
`go build/vet/test/gofmt` clean; **live /metrics smoke passed**.
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `internal/metrics` (Prometheus registry: counter/gauge/gaugefunc/histogram) | ✅ | dependency-free text exposition; bounded cardinality | `metrics_test` (5) | ✅ |
| `api` RED + `/metrics` + structured logs | ✅ | golden signals; **route-template labels, no PII** | `metrics_test` | ✅ |
| `engine` provider/cost/fields metrics (incl. breaker_open/blocked) | ✅ | enrichment KPIs | `engine_test` | ✅ |
| `job.Queue.Depth` gauge + webhook counter (cmd) | ✅ | saturation + delivery | live smoke | ✅ |

### Slice 08 — calibration + bandit routing (learned components) (Go) — `IMPLEMENTED` 2026-07-01 (`docs/30`)
`go build/vet/test/gofmt` clean; **closed-loop unit-verified** (40-record learning run). Governing invariant held: model proposes, gate disposes.
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `internal/calibrate` (isotonic PAVA + per-pair Calibrator) | ✅ | monotonic, opt-in, offline-fit; applied pre-fusion; **provenance keeps raw score** | `isotonic_test` (3) | ✅ |
| `internal/bandit` (Beta-Thompson + conservative floor + seeded Scorer) | ✅ | learns; no-data⇒prior; replayable; dep-free (MT Gamma) | `bandit_test` (4) | ✅ |
| `router.Scorer` seam (`WithScorer`) | ✅ | reorders cascade; default=static prior; **no import cycle** | `plan_test` | ✅ |
| `engine.WithCalibrator`/`WithBandit` (update on real calls only) | ✅ | calibration reflected in resolved value; bandit learns; G1–G5 untouched | `engine_test` (2) | ✅ |
| `cmd/enrichapi` closed-loop wiring (per-request seeded scorer) | ✅ | live bandit-scored plans, race-free | build | ✅ |

### Slice 09 — real JWT auth (verified signed tokens) (Go) — `IMPLEMENTED` 2026-07-01 (`docs/31`)
`go build/vet/test/gofmt` clean; **live auth-matrix smoke passed** (202/403/401). Hardens G1: the tenant principal is now a cryptographically verified claim.
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `internal/auth` (JWT verifier: HS256+RS256, kid rotation) | ✅ | alg pinned by key (defeats none/confusion); exp/nbf/iss/aud; constant-time; **tenant_id required** | `jwt_test` (5) | ✅ |
| `internal/auth/authtest` (test-only signer) | ✅ | HS256/RS256/none/confusion token forging | (support) | ✅ |
| `api.JWTAuthenticator` + `Server.WriteScope` scope authz | ✅ | verified→principal; missing scope ⇒ 403; failures ⇒ 401 no-leak | `jwt_integration_test` | ✅ |
| `tenant.Principal.Scopes`/`HasScope` | ✅ | OAuth2 scopes on the principal | (used) | ✅ |
| `cmd/enrichapi` JWT env wiring (fallback to dev tokens) | ✅ | JWT_HS256_SECRET/ISSUER/AUDIENCE/KID | live smoke | ✅ |

### Slice 10 — Postgres store + live tenant-isolation (RLS) proof (Go) — `IMPLEMENTED` 2026-07-01 (`docs/32`)
Mainline `go build/vet/test/gofmt` clean; **LIVE RLS test passed on PostgreSQL 17.10**. ⭐ **G1 datastore release-blocker (docs/21 §1) SATISFIED + live-verified.**
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `internal/pg` (stdlib pgwire v3 client: simple+extended protocol) | ✅ | zero-dep; bound params (no injection); error-recovery | `conn_integration_test` (live) | ✅ |
| `internal/pgstore` (FieldVersions over pg; tenant GUC per txn) | ✅ | tenant from principal only; writes stamped from GUC; fail-closed | `rls_integration_test` (live) | ✅ |
| RLS proof (as **non-superuser** `app_rls`) | ✅ | cross-tenant read=0 rows; WITH CHECK blocks cross-tenant write; store isolates via principal | `rls_integration_test` (live) | ✅ |
| `scripts/run-rls-test.sh` reproducible harness | ✅ | ephemeral cluster or `WATERFALL_PG_DSN` | (harness) | ✅ |

### Slice 11 — full-stack end-to-end test (live) (Go) — `IMPLEMENTED` 2026-07-01 (`docs/33`)
Mainline `go build/vet/test/gofmt` clean; **full-stack E2E passed live on PostgreSQL 17**. Real JWT → API → queue → engine → live-PG(RLS) → signed webhook.
| Gate (asserted black-box over HTTP) | Result |
|---|---|
| G1 tenant isolation — other tenant sees 0 fields (live RLS) | ✅ |
| G2 idempotency — identical 2nd request = 0 new provider calls | ✅ |
| G4 cost ceiling — `cost_ceiling:2` commits ≤ 2 (no overspend) | ✅ |
| G5 provenance — value read back from PG carries provider/idempotency_key/confidence | ✅ |
| Webhook — signature-valid, tenant-bound delivery on completion | ✅ |

### Slice 12 — Postgres G2/G4 ledgers + connection pool (Go) — `IMPLEMENTED` 2026-07-01 (`docs/34`)
Mainline `go build/vet/test/gofmt` clean (89 tests); **5 live integration tests pass on PostgreSQL 17**. ⭐ **All five gates now datastore-enforced with RLS.**
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `pgstore` IdempotencyLedger (G2) | ✅ | INSERT ON CONFLICT DO NOTHING (first-writer-wins); JSONB result; RLS-isolated | `ledgers_integration_test` (live) | ✅ |
| `pgstore` CostLedger (G4) | ✅ | atomic guarded `UPDATE ... WHERE committed+amt<=ceiling`; rejection = no change; RLS-isolated | `ledgers_integration_test` (live) | ✅ |
| `internal/pg` `Pool` (bounded conn pool) | ✅ | token-capped open conns; reuse; broken-eviction; per-txn tenant GUC | `pool_test` (mainline) | ✅ |
| E2E on full Postgres store | ✅ | G2/G4/G5 all datastore-durable; ledger tables non-empty post-run | `fullstack_integration_test` (live) | ✅ |

### Slice 13 — Postgres transactional-outbox durable queue (Go) — `IMPLEMENTED` 2026-07-01 (`docs/35`)
Mainline `go build/vet/test/gofmt` clean; **live crash-safety test passed on PostgreSQL 17**. Replaces the file-WAL durable queue (Slice 03) with a Postgres outbox.
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `migrations/0002_job_outbox.sql` | ✅ | payload jsonb + pending intent + claimed_at; RLS + FORCE; partial pending index | (applied) | ✅ |
| `pgoutbox.Store` (job.Store + job.Submitter) | ✅ | Submit durable-capture (ON CONFLICT DO NOTHING); Put clears pending iff terminal, same UPDATE; RLS-scoped | `outbox_integration_test` (live) | ✅ |
| `pgoutbox.Relay` (FOR UPDATE SKIP LOCKED) | ✅ | competing-consumers claim; visibility-timeout crash recovery | `outbox_integration_test` (live) | ✅ |
| crash + redelivery + G2 dedupe | ✅ | at-least-once → exactly-once-effective; outcome round-trips JSONB; tenant isolation | `outbox_integration_test` (live) | ✅ |

### Slice 14 — SCRAM-SHA-256 auth + TLS + migration runner (Go) — `IMPLEMENTED` 2026-07-01 (`docs/36`)
Mainline `go build/vet/test/gofmt` clean (91 tests); **SCRAM/TLS/migration tests passed live on PostgreSQL 17**. Zero new deps.
| Module | Code | Property | Test | Done |
|--------|------|----------|------|------|
| `pg/scram.go` (SCRAM-SHA-256, RFC 7677) | ✅ | PBKDF2 + client-proof + mutual-auth verifier; SASL wired into startup | `scram_test` (RFC vector, mainline) + `TestConn_SCRAM` (live) | ✅ |
| `pg` TLS (`SSLRequest` + `Config.TLS`/`sslmode`) | ✅ | encrypted transport; libpq sslmode semantics | `TestConn_TLS` (live, pg_stat_ssl) | ✅ |
| `internal/pgmigrate` (ordered runner) | ✅ | schema_migrations; atomic-per-file; idempotent | `TestApply_OrderedAndIdempotent` (live) | ✅ |
| migrations 0001/0002 de-transactioned | ✅ | runner owns the txn; direct apply still atomic | (regression: all live tests) | ✅ |

### Slice 15 — real-provider HTTP smoke + pinned fixtures (Go) — `IMPLEMENTED` 2026-07-01 (`docs/37`)
Mainline `go build/vet/test/gofmt` clean (94 tests). Narrows the vendor no-fabrication gap to one tested, labelled place.
| Item | Verified | Test | Done |
|------|----------|------|------|
| Auth scheme + injection (query / header / basic) end-to-end | ✅ VERIFIED | `adapters_test` + `live_smoke_test` | ✅ |
| HTTP status→error-class matrix (incl. 404→NOT_FOUND, 401/429/5xx) | ✅ VERIFIED | `TestAdapters_StatusErrorMatrix` | ✅ |
| SSRF choke on the adapter egress path (`NewEgressClient` → block) | ✅ VERIFIED | `TestAdapter_EgressSSRFBlocked` | ✅ |
| Vendor JSON field names | ⚠️ UNVERIFIED (pinned as `testdata/` fixtures + promotion path) | `TestAdapters_DecodeRecordedFixtures` | ✅ |

### Slice 16 — wire the full Postgres durable path into the binary (Go) — `IMPLEMENTED` 2026-07-01 (`docs/38`)
Mainline `go build/vet/test/gofmt` clean (94 tests); **live crash-recovery proven through the real binary on PostgreSQL 17**.
| Item | Code | Property | Proof | Done |
|------|------|----------|-------|------|
| `cmd/enrichapi` Postgres branch | ✅ | `pgstore` store + `pgoutbox` outbox + `Relay`, gated on `POSTGRES_DSN` | `scripts/crash-recovery-test.sh` | ✅ |
| Self-bootstrap (admin DSN) | ✅ | migrations + idempotent `app_rls`/`relay` role provisioning at startup | (bootstrap succeeds live) | ✅ |
| RLS enforced at runtime | ✅ | app connects as NON-superuser `app_rls`; relay `BYPASSRLS` for claim only | (round-2 records tenant-scoped) | ✅ |
| Crash recovery through the real binary | ✅ | `kill -9` mid-flight → restart → outbox relay recovers | **3 pending at crash → 40/40 recovered, 40 ledger rows (G2)** | ✅ |

### Slice 17 — outbox dead-letter queue + max-attempts (Go) — `IMPLEMENTED` 2026-07-01 (`docs/39`)
Mainline `go build/vet/test/gofmt` clean (94 tests); **DLQ path proven live on PostgreSQL 17; crash-recovery still passes**.
| Item | Code | Property | Proof | Done |
|------|------|----------|-------|------|
| Migration `0003_outbox_dlq` | ✅ | `attempts`/`dead`/`last_error` + partial dead index | `TestApply_OrderedAndIdempotent` (now 3 migrations) | ✅ |
| `Relay.claim` attempt-count + park | ✅ | claim increments attempts; exceed max → dead=true, pending=false, not delivered | `TestPGOutbox_DeadLetterAfterMaxAttempts` | ✅ |
| `WithMaxAttempts` / `WithDeadLetterHook` | ✅ | default 10; hook fires once per parked row | (hook asserted exactly once, live) | ✅ |
| `Store.DeadLetters` + `GET /v1/dead-letters` | ✅ | tenant-scoped (RLS) DLQ inspection | (G1: tenant-B sees none, live) | ✅ |
| `cmd` wiring | ✅ | `OUTBOX_MAX_ATTEMPTS`, `outbox_dead_letter_total` metric + Warn log, DLQ endpoint adapter | crash harness still PASS | ✅ |

### Slice 18 — DLQ redrive / replay (Go) — `IMPLEMENTED` 2026-07-01 (`docs/40`)
Mainline `go build/vet/test/gofmt` clean (94 tests); **redrive→replay proven live on PostgreSQL 17**.
| Item | Code | Property | Proof | Done |
|------|------|----------|-------|------|
| `Store.Redrive` | ✅ | RLS-scoped reset (dead→false, pending→true, attempts→0); payload untouched (same job re-runs, G2-safe) | `TestPGOutbox_RedriveReplaysParkedJob` | ✅ |
| `POST /v1/dead-letters/{id}/redrive` | ✅ | write-scoped, tenant-scoped, 404 when nothing dead, audit-logged, `dlq_redrive_total` | (endpoint + openapi) | ✅ |
| G1 on redrive | ✅ | a tenant cannot redrive another tenant's job | (tenant-B → false, live) | ✅ |
| Replay to completion | ✅ | redriven job re-delivers and reaches `succeeded` | (work_email filled, live) | ✅ |

### Slice 19 — consolidation: README, one-command demo, docs index (Go) — `IMPLEMENTED` 2026-07-01 (`docs/41`)
Documentation + orchestration only (no engine code); demo runs end-to-end; mainline unaffected.
| Item | Property | Done |
|------|----------|------|
| `README.md` (top-level) | five gates + invariant, architecture, quickstart, testing story, honest deferrals — all backed by code/tests | ✅ |
| `scripts/demo.sh` | one command: build → unit → `enrichd` provenance demo → live HTTP round-trip → auto-detected PG harnesses | ✅ |
| `docs/README.md` index | stale "awaiting approval" status replaced; slices 23–40 + top-level README indexed | ✅ |
| godoc | audited complete (every package doc'd; both cmds use `// Command`) — no change needed | ✅ |
| **harness bugfix** | `run-rls-test.sh` parallel-DB race fixed with `-p 1` (found via the demo); 9 tests now deterministic | ✅ |

### Slice 20 — config validation + startup self-check (Go) — `IMPLEMENTED` 2026-07-01 (`docs/42`)
Mainline `go build/vet/test/gofmt` clean (99 tests); **fail-fast + G1 self-check proven live on PostgreSQL 17**.
| Item | Code | Property | Proof | Done |
|------|------|----------|-------|------|
| `internal/config` | ✅ | validates PORT/DSNs/max-attempts/JWT + coherence; reports ALL errors joined | `config_test` (4) + live (bad config refuses to start) | ✅ |
| `startupSelfCheck` (G1) | ✅ | refuses to start as a superuser/BYPASSRLS role; verifies schema present | live (superuser DSN → exit 1) | ✅ |
| `pg.RolePrivileges` / `pgmigrate.Pending` | ✅ | detect RLS-bypass role / migration drift | `TestRolePrivileges`, `TestPending_ReportsUnapplied` | ✅ |
| `GET /readyz` + `pgstore.Ping` | ✅ | ready only when datastore reachable; distinct from `/healthz` | `TestReadyz` (200/200/503) + live | ✅ |

Integration tests are build-tagged (`-tags integration`) and gated on `WATERFALL_PG_DSN` (SCRAM/TLS additionally on `WATERFALL_PG_SCRAM_DSN`/`WATERFALL_PG_TLS_DSN`); run via `scripts/run-rls-test.sh` (`-p 1`; pg conn + pool + **role-privileges** + SCRAM + TLS + G2/G4 ledgers + RLS + outbox crash-safety + outbox dead-letter + DLQ redrive + migration runner + **migration-drift** + full-stack E2E — 13 live tests). The end-to-end **crash-recovery through the real binary** runs via `scripts/crash-recovery-test.sh`. **`scripts/demo.sh`** runs the whole tour in one command.

### Waterfall Management Dashboard — Phases P0–P12 — `IMPLEMENTED` 2026-07-06 (`docs/waterfall-dashboard/12` §5)
Mainline `go build ./... && go vet ./...` clean (47 packages); `scripts/run-rls-test.sh` release-blocker
suite green on PostgreSQL 17.10 (RLS zero-rows on every dashboard table + fuzz + G2 replay + CSRF/idempotency
negatives); web `tsc --noEmit` + **192 vitest** (22 files) + no-orphan-UI + **111.2 KB-gz** production build green;
**live boot smoke passed** (dashboardd boots against an ephemeral PG17 with bootstrap → serves SPA + `/healthz`
`/readyz` `/metrics`, rejects unauth admin 401, pbkdf2 login operator→mfa_required / tenant_user→ok, six
authenticated operator reads 200). One commit per phase on branch `waterfall`; the deliverable docs 00–14 are ACCEPTED.

| Phase | Built (packages / migrations) | Acceptance evidence (test) | Status |
|-------|-------------------------------|----------------------------|--------|
| P0 | identity/tenancy/session/audit spine; `dash/{db,httpx,rbac,security,secrets,audit}` + `cmd/dashboardd` (migr 0004) | RLS zero-rows all 8 tables; login→MFA→audit-verify E2E; CSRF 403; idem 409 (`TestDashRLSZeroRows`, `TestDashLoginMFAAndSecurity`) | IMPLEMENTED |
| P1 | providers catalog + keys/pools + envelope secrets + 1k import (migr 0005) | 1k-key import sealed, **zero plaintext**; envelope round-trip; Class-P RLS (`TestKeysImportSealAndRLS`, `TestProvidersLifecycleAndRLS`) | IMPLEMENTED |
| P2 | `dash/rotation` engine (strategies, batched leases, KM-3 machine) | no over-lease @50 goroutines; ≥10k sel/s (measured **24.7M**); engine E2E (`TestRotationLeaseNoOverLease`, `TestRotationEngineE2E`, `BenchmarkPoolSelect`) | IMPLEMENTED |
| P3 | config versioning + routing + workflows (migr 0006) | lifecycle+rollback; concurrent-publish 409; dry-run zero egress (`TestConfigLifecycleAndRLS`, `TestConcurrentPublishConflict`) | IMPLEMENTED |
| P4 | telemetry backbone + health center + approvals + leader loops (migr 0007/0008/0009) | 100k refold **byte-identical**; approval exactly-once; health timeline (`TestTelemetryFoldRefoldIdentical`, `TestApprovalsExactlyOnce`, `TestHealthTimelineFoldAndNoData`) | IMPLEMENTED |
| P5 | queues + workers read model + pgoutbox redrive + heartbeat client | replay idempotent; drain converges; lost detection (`TestQueuesReplayIdempotent`, `TestWorkersDrainConverges`, `TestWorkersLostDetection`) | IMPLEMENTED |
| P6 | cost analytics + alerts evaluator/notifier | group-bys match ledgers; SSRF test-send blocked; fire/dedupe/resolve (`TestCostGroupBysMatchLedgers`, `TestAlertsTestSendSSRFBlocked`, `TestAlertsFireDedupeResolve`) | IMPLEMENTED |
| P7 | overview 2s aggregator + SSE realtime + poller + self_monitor (migr 0010) | 200-client soak-lite **p99 12.27ms** zero-drop; snapshot→delta; leader failover (`TestSSESoakLite`, `TestOverviewSnapshotThenDelta`, `TestOverviewAggregatorFailover`) | IMPLEMENTED |
| P8 | SPA scaffold: design system, typed api client, SSE manager, auth flow | `npm run build` green; client idem+CSRF; SSE routing (vitest `client`/`sse`) | IMPLEMENTED |
| P9 | FE providers/keys/rotation/health screens | key grid @1k virtualized; status totality; no-orphan-UI (vitest `keyGrid`/`status`/`badges`) | IMPLEMENTED |
| P10 | FE routing/workflows/queues/workers screens | routing publish+rollback; DLQ redrive; convergence (vitest `lifecycle`/`redrive`/`convergence`) | IMPLEMENTED |
| P11 | FE cost/alerts/security/approvals/settings + a11y | no-orphan-UI across ALL routes; cost/alerts/approvals models (vitest) | IMPLEMENTED |
| P12 | hardening: measured load numbers, security pass, runbook validation, docs→ACCEPTED | live boot smoke; L1–L4 measured (doc 13 §6); secret scan clean; docs 00–14 flipped ACCEPTED | IMPLEMENTED |

Residual full-scale / multi-instance / chaos / live-Playwright items are deferred to a staging load-lab
(doc 12 §5 OI-P12-1..3). Integration suite is `-tags integration`, gated on `WATERFALL_PG_DSN`, run serially
(`-p 1`) via the extended `scripts/run-rls-test.sh` (`PGBIN=…/pg17 bash scripts/run-rls-test.sh`).

**Next slices (not started):** Postgres G2/G4 ledgers + connection pool (extend datastore RLS to idempotency/cost) · SCRAM/TLS in pg client + migration runner · distributed durable log (Kafka/Redpanda + DB outbox/CDC) · OpenTelemetry tracing + Grafana dashboards · webhook-retry topic + registration API · JWKS discovery + RS256 PEM/mTLS + token revocation · egress as a separate service + network policy · live-vendor fixture validation · online calibration + label feedback loop + durable/shared bandit state. See `docs/23` §5 · `24` §6 · `25` §5 · `26` §4 · `27` §4 · `28` §5 · `29` §5 · `30` §5 · `31` §5 · `32` §5.

## Open gate items (blocking)
| ID | Phase | Item | Type | Owner | Status |
|----|-------|------|------|-------|--------|
| GI-1 | 1–3 | Provider/competitor facts | **cited** (46 providers, `01`+`03`); residual UNVERIFIED (latency, weak identity-intel) are `ACCEPTED-RISK` | Research Agent | mostly resolved |
| PR-EXCL-1 | 3 | Human policy decision on DEPRIORITIZED providers (Kaspr/ContactOut/Coresignal) | decision | Human | ✅ resolved 2026-07-01 (compliance-gated) |
| QS-TMP-1 | 10 | Temporal Action-cost spike (ADR-0014) before unconditional adoption; fallback = hand-rolled saga+outbox | validation + decision | Cost/Scale + Human | **open** (flagged; safe fallback exists) |
| GI-2 | 0 | Optional `.claude/` mirror of skills/commands | enhancement | — | deferred |
