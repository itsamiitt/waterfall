# ADR 0012 — API protocol strategy: REST + webhooks external, gRPC internal, GraphQL deferred

- **Status:** Accepted
- **Date:** 2026-07-01
- **Deciders:** Principal Backend Engineer, Senior Product Manager
- **Phase:** 7 · **Source:** `docs/07-API-Gateway.md`

## Context
We must expose enrichment to many tenants (single, batch, bulk, async job, streaming) and choose which
protocols to support externally vs internally. Supporting everything (REST+GraphQL+gRPC externally) multiplies
surface, auth, docs, and security review with little tenant benefit early.

## Options considered
| Option | Pros | Cons |
|--------|------|------|
| **REST + webhooks external; gRPC internal; GraphQL deferred (chosen)** | ubiquitous client support, simplest auth/versioning/caching, smallest external attack surface; gRPC where internal perf matters | GraphQL flexibility not offered day 1 |
| REST + GraphQL + gRPC all external | max flexibility | 3× surface/auth/security review; SSRF/DoS surface grows; premature |
| gRPC-first external | perf | poor browser/partner support; steeper client onboarding |

## Decision
**External:** REST/JSON (primary) for single + **batch** (sync, small N) + **bulk async jobs**
(`202`+job_id+poll/webhook); **webhooks** for async result/event delivery (HMAC-signed, idempotent
receiver expectation); optional **streaming** export (chunked/NDJSON or SSE) for large result pulls.
**Internal:** gRPC allowed between data-plane services where latency/throughput justify it. **GraphQL:
deferred** (roadmap `22`) — revisit if tenants need flexible field selection at scale.
**Idempotency-Key header required on all writes/jobs** (G2). **API versioning** via URL major version
(`/v1`) + deprecation policy.

## Rationale
REST+webhooks covers every required interaction (single/batch/bulk/async/streaming) with the broadest client
support and the smallest external attack surface, which matters given SSRF/tenant-isolation risk. gRPC stays
internal where it pays off; GraphQL's flexibility isn't worth its early cost/surface.

## Consequences
- Positive: one external auth/versioning/caching model; minimal surface to security-review; easy partner onboarding.
- Negative/accepted: no external GraphQL day 1 (deferred, not rejected); batch size caps needed to bound DoS.

## Verification
Contract tests against the OpenAPI spec; webhook signature + idempotent-replay tests; rate-limit/batch-cap DoS tests (`21`).
