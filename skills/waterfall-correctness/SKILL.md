---
name: waterfall-correctness
description: >-
  The hard gates every piece of enrichment logic MUST pass before it is considered correct:
  (1) tenant isolation on every query, (2) idempotency on every external/provider call,
  (3) bounded + timeout-wrapped provider calls, (4) cost ceiling enforced before execution,
  (5) confidence + provenance recorded on every written field. Invoke on every enrichment module.
version: 1.0.0
---

# Skill: waterfall-correctness

## Purpose
A single, enforceable correctness contract for all enrichment logic (router, execution engine,
verification, identity resolution, persistence). If any of the five gates is unmet, the code is
**not correct** regardless of test pass rate.

## The five hard gates

### G1 — Tenant isolation on EVERY query
- Every read/write carries `tenant_id`; DB enforces **Row-Level Security**, not just app code.
- No query, cache key, queue message, log line, or metric may leak cross-tenant data.
- Cache keys are namespaced `tenant_id:` ; queue messages include `tenant_id`; provider result
  cache is shared **only** for non-PII firmographics if a policy explicitly allows it (default: not).
- Test: a tenant-A token can never read tenant-B rows (negative test required).

### G2 — Idempotency on EVERY external/provider call
- **Canonical Idempotency Key (single source of truth, used verbatim in `04`/`09`/`10`/`erd.mmd`):**
  `idempotency_key = hash(tenant_id, record_id, field, provider, normalized_request_params, config_version)`.
  A **deliberate re-fetch/refresh** (e.g. TTL expiry, a "re-verify" request) is expressed by folding an
  explicit `as_of`/refresh token into `normalized_request_params` (or a bumped `config_version`), which
  yields a *new* key — so intended refreshes are not suppressed while accidental replays are.
- Replays (retry, redelivery, at-least-once queue) must not double-charge credits or double-write.
- Persist a request ledger keyed by idempotency key; short-circuit on a prior terminal result. The key is
  stored on the resulting `field_versions` row (G5) for audit/replay.

### G3 — Bounded + timeout-wrapped provider calls
- Every provider call has: connect timeout, total timeout, max attempts, and a **circuit breaker**.
- No unbounded retries; backoff = exponential + full jitter; cap total attempts and total wall-time.
- A provider call that exceeds its timeout is a failure that the router may fall back from — never a hang.

### G4 — Cost ceiling enforced BEFORE execution
- Before any provider call: check per-record, per-job, and per-tenant **Cost Ceiling** + remaining
  credits/quota. If the next call would exceed a ceiling, **stop** (do not call) and record reason.
- Cost is reserved (debited optimistically) before the call and reconciled after (refund on miss if
  the provider does not charge for misses — provider-specific, from `03`).

### G5 — Confidence + provenance on EVERY written field
- Every written Field stores: value, `confidence` (0..1), `provider`, `verified_date`/fetched_at,
  cost, idempotency key, and the run/job id. No field is written "bare".
- Conflict resolution + merge must keep provenance for the losing values (history), not discard them.

## SSRF note (ties to `18-Security.md`)
Although this engine is API-first, **any** field that is a URL/domain/host (company_domain, a
provider that takes a callback URL, a tenant-supplied webhook) is an SSRF vector. All outbound
fetches go through an **egress allow-list + DNS-rebinding-safe resolver**; never fetch a
tenant/record-supplied URL directly. This is part of G3's "bounded" requirement.

## Per-module checklist (must be PASS to mark a module done)
- [ ] G1 tenant isolation: RLS on, negative cross-tenant test passes.
- [ ] G2 idempotency: key derivation correct; replay test shows no double-charge/double-write.
- [ ] G3 bounded: timeouts + breaker + capped jittered backoff present; no unbounded loop.
- [ ] G4 cost ceiling: pre-flight check + reservation + reconciliation present; over-ceiling stops.
- [ ] G5 provenance: every write carries value+confidence+provider+cost+timestamp+run id.
- [ ] SSRF: no direct fetch of record/tenant-supplied host; egress allow-list enforced.
