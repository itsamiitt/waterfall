# 18 — Security

**Status:** `IN-REVIEW` · **Owner:** Staff Security Engineer · **Last updated:** 2026-07-01
**Gated by:** [Security Auditor](../agents/security-auditor.md) · [waterfall-correctness](../skills/waterfall-correctness/SKILL.md) · `/security-audit`

> **Two highest-risk areas, prioritized: (P1) tenant isolation, (P2) SSRF in the enrichment fetch path.**
> This doc consolidates the security model; the SSRF spec is canonical in [`13`](13-Proxy-Management.md).

## 1. Tenant isolation (P1) — two layers (ADR-0010)
1. **Identity binding:** tenant_id extracted **only** from the authenticated principal (mTLS/SPIFFE or
   signed JWT), bound as a signed/immutable request context — **never** from request body/params.
2. **Enforcement floor:** Postgres `FORCE ROW LEVEL SECURITY`; hot-path role has **no `BYPASSRLS`**;
   `SET LOCAL app.current_tenant` per transaction. Redis keys tenant-prefixed; Kafka partitions keyed by
   tenant_id; identity clusters + bandit posteriors tenant-scoped; cache/metrics namespaced.
   - **Analytics store (ClickHouse) has no Postgres-RLS**, so it uses a compensating control (`06` §2):
     ClickHouse row policies + a server-side mandatory `tenant_id` predicate + a CI test that no
     analytics/dashboard query runs un-scoped. Operator cross-tenant analytics is an audited RBAC capability.
- **IDOR:** every resource id is validated against the principal's tenant. **CI must run a cross-tenant
  negative test** (tenant A cannot read/modify B) on every resource — a release blocker.

## 2. SSRF (P2) — canonical in `13`
Single egress choke (only the egress-proxy has an internet route; everything else default-deny). FQDN
allow-list + DNS-rebinding-safe resolver (resolve→pin→revalidate, redirects re-checked) blocking
metadata/RFC1918/loopback/ULA/CGNAT; HTTPS-only; provider base URLs globally allow-listed, **tenant webhook
hosts per-tenant-scoped** (never from request data); provider keys injected at the proxy. Outbound tenant
webhook URLs are **untrusted input** → full SSRF checks, and the target must be in the **delivering
tenant's** registered set (server-side from `tenant_id`) so PII cannot egress cross-tenant (`13` §6).

## 3. AuthN / AuthZ
- **OAuth2 / JWT** (short-lived + rotated) or mTLS for machine tenants; single-flight token refresh.
- **RBAC + ABAC** enforced **server-side** (roles: operator/tenant_admin/tenant_user; attributes: tenant,
  region, plan tier). No client-trusted authorization.

## 4. Secrets & encryption
- Secrets in **secrets manager / Vault**; never in code, logs, error messages, diagrams, or the API gateway.
  Provider keys leased just-in-time to the egress-proxy (`12`); rotation scheduled + on-compromise.
- **Encryption in transit** (TLS everywhere) + **at rest** (KMS-backed). PII fields identified + protected;
  field-level encryption for the most sensitive PII where required.

## 5. Data residency & compliance
- PII routed/stored per **regional cell** (`04`/`19`); cross-region transfer is policy-gated; cross-region
  learning shares **non-PII** signals only.
- **Compliance map:** SOC2 Type II, ISO 27001, GDPR (DPA, DSAR, suppression), CCPA/CPRA — **plus, per
  [ADR-0009](../adr/0009-provider-inclusion-exclusion-criteria.md): data-broker registration (e.g. CA/VT),
  DNC/TPS suppression lists, and per-record consent/provenance metadata** (G5).
- **DEPRIORITIZED providers** (Kaspr/ContactOut/Coresignal) require a **compliance review** before
  production use (PR-EXCL-1 resolved: compliance-gated); their provenance persisted per record.

## 6. Audit, rate limiting, abuse
- **Audit log** on sensitive/cross-tenant actions; logs scrubbed of secrets/PII. **Immutability is
  mechanized, not asserted:** append-only table with a **per-tenant hash-chain** (each row carries
  `prev_hash`; tamper is detectable) written by an INSERT-only role (no UPDATE/DELETE grant), periodically
  anchored/shipped to **WORM object storage** (S3 Object Lock / equivalent) for retention + legal hold.
- Per-tenant + per-endpoint **rate limiting** + batch caps (DoS); bot/abuse controls at the edge.

## 7. Disaster recovery
- Backups (Postgres PITR, config-as-data snapshots, object-store for tiered Kafka/ClickHouse).
- **RPO ≤ 5 min, RTO ≤ 1 hr** (targets; validated by restore drills — `21`); key-loss recovery via KMS/Vault
  procedures; regional-cell failover.

## 8. Threat model (STRIDE, per trust boundary — to expand at impl)
| Boundary | Top threats | Mitigation |
|----------|-------------|-----------|
| client↔edge | spoofed tenant, token theft, DoS | mTLS/JWT, short tokens, rate limit |
| edge↔modulith | tenant-context tampering | signed immutable context |
| modulith/workers↔DB | cross-tenant read (IDOR) | FORCE RLS, no BYPASSRLS, negative tests |
| workers↔egress↔internet | **SSRF**, key exfiltration | choke point, allow-list, keys at proxy |
| provider webhooks in | forged/duplicate events | HMAC verify, idempotent receiver |
| offline learning | PII leakage cross-tenant | non-PII-only sharing, tenant-scoped posteriors |

## 9. Open items
| ID | Item | Status |
|----|------|--------|
| SE-1 | SSRF resolver/allow-list spec | ✅ (`13`, canonical) |
| SE-2 | RLS policy spec | ✅ §1 (+ `06`) |
| SE-3 | Compliance control map (+ data-broker/DNC/consent) | ✅ §5 (certify at impl) |
| SE-4 | DR RPO/RTO + restore test | drafted §7; drill in `21` |

## 10. Reviewer result (`/security-audit` Phase 18)
| Check | Result |
|-------|--------|
| Tenant isolation two-layer + CI negative test (P1) | PASS |
| SSRF choke guaranteed by network policy (P2) | PASS |
| tenant_id from principal only; RBAC/ABAC server-side | PASS |
| Secrets in Vault; never logged; keys at egress | PASS |
| Encryption in transit + at rest (KMS); PII protected | PASS |
| Residency + compliance map incl. data-broker/DNC/consent | PASS |
| Audit trail + DR (RPO/RTO) | PASS |
| STRIDE per boundary | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded — full adversarial pass at the Planning Completion Gate).
