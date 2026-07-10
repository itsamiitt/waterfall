# ADR 0030 — CRM outbound connectors through the single egress-proxy (roadmap)

- **Status:** Accepted (design; **roadmap** — implementation behind a later gate)
- **Date:** 2026-07-09
- **Deciders:** Lead Enterprise Solutions Architect, Staff Security Engineer, Principal Backend Engineer
- **Phase:** R&I (Research & Intelligence) · **Preserves:** ADR-0010 · **Consumes:** ADR-0028 (`crm_ready`)

## Context
The Dossier carries a normalized `crm_ready.{account, contact}` projection (ADR-0028) meant for direct
CRM ingestion. The roadmap needs to **push** that projection outbound to a tenant's CRM
(Salesforce/HubSpot) — a **new trust direction**: today all provider traffic is *inbound data pull*;
CRM push is *outbound customer-data write*.

**ADR-0010** is explicit that the **egress-proxy is the *sole* SSRF boundary and provider-key
custodian** — the only internet route out of the platform, and the only place credentials are
injected. The naive design (a second `crm-egress` deployable, or pushing directly from a control-plane
module) would either **split the sole-boundary invariant** or **put CRM OAuth tokens in worker
memory** — both regressions. This ADR exists to prevent that.

## Options considered
| Option | Pros | Cons | Key tradeoff |
|--------|------|------|--------------|
| A. New second egress deployable (`crm-egress`) | clean separation of push vs pull | **two internet routes** — breaks ADR-0010's *sole* SSRF boundary; duplicates key custody + SSRF logic | separation vs the one-boundary invariant |
| B. Push directly from a control-plane module | fewer hops | **bypasses the egress-proxy**; CRM tokens live in control-plane memory; new un-choked SSRF surface | latency vs security |
| **C. Route CRM push through the existing egress-proxy as a new outbound *direction* (chosen)** | preserves the single boundary + single key custodian; reuses SSRF choke, breaker, cost, idempotency | the egress-proxy must learn an outbound "write" mode + CRM host allow-list | reuse/discipline vs a small egress extension |

## Decision
**CRM push is a new outbound direction of the existing egress-proxy, not a new deployable.**

- **`internal/crm` is a control-plane module** (connection config + field maps only). It owns
  `crm_connections`, `crm_field_maps`, `crm_push_ledger` (**migration 0018**; FORCE RLS, no BYPASSRLS;
  CRM OAuth secrets sealed via the ADR-0017 envelope backend — reference only, no plaintext).
- **The push itself is a CRM connector adapter executed through the egress-proxy** — same
  `AuthDescriptor` + egress key-injection (CRM OAuth token attached at the boundary, never in the
  control-plane), same SSRF host allow-list (extended to the CRM API hosts), same breaker. **There is
  no second internet route.**
- **Gates.** G1 tenant isolation on all `crm_*` (a push writes only the pushing Tenant's data);
  **G2 idempotency on every push** (`crm_push_ledger`, key = `hash(tenant, connection, record,
  field_map_version, dossier_version)`) so a retry/redelivery never double-writes the CRM; G3 bounded
  (`CallPolicy` + breaker); G4 push cost/rate ceiling; G5 provenance (what was pushed, when, from which
  Dossier version, outcome). Idempotency-Key required on the admin write that configures/triggers a
  push (ADR-0012).
- **Direction of trust.** CRM push carries **customer data outbound**; it is subject to the same
  content-trust + PII/DSAR baseline as dossier-storing modules (`docs/research-intelligence/09`) — a
  DSAR delete cascades to what was pushed where policy requires.
- **Scope:** design is accepted now; **implementation is roadmap** (Slice 27+), behind its own approval
  gate. Field maps + connector specifics are detailed in `docs/research-intelligence/15`.

## Rationale
Option C is the only one that adds CRM push **without** weakening ADR-0010's central guarantee: one
internet boundary, one key custodian, one SSRF choke. Making CRM a *direction* of the existing
egress-proxy means push inherits the breaker, cost accounting, idempotency, and SSRF allow-list for
free, and CRM tokens stay at the boundary exactly like provider keys. We rejected a second egress
service (Option A — two boundaries defeats the invariant) and direct control-plane push (Option B —
bypasses the choke and leaks tokens into worker memory).

## Consequences
- **Positive:** single SSRF boundary preserved; CRM tokens custodied at the egress tier; push is
  bounded, idempotent, costed, and provenanced like every other call.
- **Negative / accepted:** the egress-proxy gains an outbound "write" mode + a CRM host allow-list;
  CRM connector semantics (upsert keys, rate limits) are per-CRM work, deferred to the roadmap. Accepted.
- **Follow-ups / new ADRs triggered:** none required for the single-boundary decision; per-CRM connector
  detail lands with the roadmap implementation.

## Verification
- **Single-boundary audit** (`/security-audit`): there is **no** second egress deployable and **no**
  control-plane code opening a direct outbound socket; every CRM push traverses the egress-proxy and its
  SSRF allow-list; CRM tokens are envelope-sealed, injected only at egress.
- **Idempotency:** a redelivered push with the same key is a no-op against the CRM (asserted via
  `crm_push_ledger` + a fake CRM sink); an RFC1918 CRM host is refused by the SSRF guard.
- **Gates:** G1 cross-tenant push isolation (Tenant A cannot push into Tenant B's connection). Push
  throughput/limit numbers stay `UNVERIFIED` until the roadmap implementation measures them.
