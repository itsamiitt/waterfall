# Architecture Decision Records (ADRs)

Every non-trivial decision — especially where roles conflict (latency vs correctness, cost vs
coverage, speed vs compliance) — is recorded here as one immutable file, using the
[template](0000-adr-template.md) (Nygard-style). ADRs are append-only: to change a decision, write a
new ADR that **supersedes** the old one (and mark the old one superseded). Never edit a decided ADR's
Decision section in place.

## Index
| ADR | Title | Status |
|-----|-------|--------|
| [0000](0000-adr-template.md) | ADR template | Template |
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-api-first-no-scraping.md) | API-first only; no scraping/browser automation | Accepted |
| [0003](0003-plan-first-gated-process.md) | Plan-first, gate-driven delivery process | Accepted |
| [0004](0004-identity-resolution-tiered.md) | Identity resolution: tiered deterministic→blocking→Fellegi–Sunter→cost-gated ML | Accepted |
| [0005](0005-confidence-calibrate-then-fuse.md) | Confidence: calibrate-then-fuse (log-odds) + SPRT stop | Accepted |
| [0006](0006-conflict-resolution-deterministic-online.md) | Conflict/merge: deterministic online + offline-learned weights + PROV | Accepted |
| [0007](0007-provider-ordering-pandora-cascade.md) | Provider ordering: Pandora reservation-value cascade + SPRT stop | Accepted |
| [0008](0008-adaptive-routing-thompson-guardrailed.md) | Adaptive routing: Thompson sampling inside deterministic G3/G4 gate | Accepted |
| [0009](0009-provider-inclusion-exclusion-criteria.md) | Provider inclusion/exclusion criteria (API-first vs data provenance) | Accepted |
| [0010](0010-architecture-style-modulith-dataplane.md) | Architecture style: modulith control-plane + elastic stateless data-plane | Accepted |
| [0011](0011-datastore-postgres-rls-pool.md) | Datastore & tenant-isolation: PostgreSQL RLS-pool (+ Redis + ClickHouse) | Accepted |
| [0012](0012-api-protocol-strategy.md) | API protocol strategy: REST + webhooks external, gRPC internal, GraphQL deferred | Accepted |
| [0013](0013-async-transport-kafka-log.md) | Async transport: Kafka-protocol log (Redpanda preferred) | Accepted |
| [0014](0014-orchestration-temporal-cost-gated.md) | Orchestration: Temporal durable execution (cost-gated), fallback hand-rolled Saga | Accepted (gated) |
| [0015](0015-cloud-and-region-topology.md) | Cloud & region topology: portability-first, AWS primary reference, regional cells | Accepted |
| [0016](0016-frontend-dependency-exception.md) | Frontend dependency exception: React + TypeScript + Vite SPA with a pinned allowlist | Accepted |
| [0017](0017-secrets-envelope-encryption.md) | Secrets at rest: AES-256-GCM envelope encryption behind a pluggable `secrets.Backend` | Accepted |
| [0018](0018-dashboard-session-model.md) | Dashboard session model: cookie sessions for the SPA, JWT for machines, mandatory TOTP MFA | Accepted |
| [0019](0019-dashboard-realtime-sse.md) | Dashboard realtime: one multiplexed SSE stream per tab over a Postgres read-poller | Accepted |
| [0020](0020-platform-tenant-table-taxonomy.md) | Platform/tenant table taxonomy: Class P/T/R, sentinel platform tenant, dual GUC, FORCE RLS everywhere | Accepted |
| [0021](0021-tenant-provisioning-path.md) | Operator Tenant provisioning via target-Tenant-bound INSERT (no BYPASSRLS) | Accepted |
| [0022](0022-store-adapter-dependency-policy.md) | Per-store adapter dependency policy: hand-roll Redis/S3/ClickHouse-HTTP; ADR-0016 exception only for Kafka/Temporal | Accepted |
| [0023](0023-adapter-registry-catalog-seed-field-vocabulary.md) | Adapter registry (single source of truth) + code→catalog seeder + Field-vocabulary extension for the 200-provider rollout | Accepted |
| [0024](0024-async-multi-credential-provider-egress.md) | Asynchronous & multi-credential provider egress (per-adapter CallPolicy, oauth2-cc, async submit→poll) | Accepted (phased) |

> All architecture decisions are recorded. **Resolved:** datastore (ADR-0011), API protocols (ADR-0012),
> queue transport (ADR-0013), orchestration (ADR-0014, cost-gated), cloud/topology (ADR-0015), secrets
> backend (ADR-0017 — closes open item KM-1/SE-secrets), frontend dependency exception (ADR-0016),
> dashboard sessions (ADR-0018), dashboard realtime transport (ADR-0019), platform/tenant table
> taxonomy (ADR-0020).
