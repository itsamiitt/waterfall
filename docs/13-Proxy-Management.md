# 13 — Egress / Proxy Management (SSRF-safe outbound)

**Status:** `IN-REVIEW` · **Owner:** Staff Security Engineer + Staff DevOps Engineer · **Last updated:** 2026-07-01
**Gated by:** [Security Auditor](../agents/security-auditor.md) (SSRF **priority**) · `/security-audit`

> **API-first (ADR-0002):** "proxy" here = **controlled outbound egress** for provider APIs + webhooks +
> regional egress — **not** scraping proxies. Its primary job is to be the single, SSRF-safe egress choke
> point. This is one of the two highest-risk areas (`00` §6); the SSRF spec here is canonical (referenced
> by `04`/`18`).

## 1. Architecture — one choke point, guaranteed by network policy
- The **egress-proxy fleet** is the **only** component with a route to the public internet. Control-plane
  modulith + execution-engine workers run in a **default-deny, no-egress** subnet (ADR-0010). No app code
  path can bypass it — the guarantee is **network policy**, not developer discipline.
- All provider calls **and** outbound webhooks traverse it. It is HA + horizontally scaled + connection-pooled
  (it is a throughput SPOF — `04` R2 — so it is treated as one).

## 2. SSRF defense (defense-in-depth)
1. **FQDN allow-list:** provider base URLs are **config/allow-listed** (global); tenant webhook hosts are
   **per-tenant-scoped** (each tenant's registered set), never derived from request/record data. A provider
   host not on the global list, or a webhook host not in the delivering tenant's set (§6), is refused.
2. **DNS-rebinding-safe resolver:** resolve **once**, **pin the IP**, validate the IP, then connect to the
   pinned IP (not re-resolve). Re-validate on every redirect hop.
3. **Block internal ranges:** reject link-local + cloud metadata (`169.254.169.254`, `fd00:ec2::254`),
   RFC1918 (10/8, 172.16/12, 192.168/16), loopback (127/8, ::1), IPv6 ULA (fc00::/7), CGNAT (100.64/10),
   `0.0.0.0/8`, and IPv4-mapped/compat IPv6 (`::ffff:0:0/96`, `::/96`). **Anti-encoding-bypass:** validation
   is performed on the **resolved binary IP**, not the host string (defeats decimal/octal/hex/short-form IP
   literals like `2130706433`, `0x7f000001`, `0177.0.0.1`); reject non-standard IP-literal encodings and
   IPv4-in-IPv6 forms at parse time before resolution.
4. **Redirect safety:** cap redirect depth; re-run allow-list + IP validation on **every** hop (a redirect
   to `169.254.169.254` or `10.x` is blocked).
5. **Scheme + port allow-list:** HTTPS only (+ explicit provider ports); no `file://`/`gopher://`/etc.
6. **Response limits:** max body size, timeout, and content-type checks to bound abuse/DoS.

## 3. Provider-key injection (secret containment)
Provider keys are **injected at the proxy** (leased from key-manager/Vault, `12`) — they **never enter
worker/adapter memory**. This is strictly better secret containment (a worker compromise yields no keys).

## 4. Regional egress & residency
- **Per-region egress IPs** for data-residency + provider **regional endpoints** (`19`).
- **Static egress IPs** where a provider requires IP-allowlisting on their side.
- Regional cells route outbound through their region's proxy (residency, `18`).

## 5. Outbound resilience
Pooled keep-alive connections + TLS session reuse (amortize handshakes across ~6,400 rps); per-host
concurrency caps (aligns with per-provider budgets, `11`); strict TLS verification; egress rate limiting;
structured egress logs (no secrets/PII).

## 6. Webhooks (both directions)
- **Inbound** provider webhooks: HMAC-verified, idempotent receiver (`09`).
- **Outbound** tenant webhooks: HMAC-signed, delivered via the proxy → same SSRF checks (a tenant webhook
  URL is untrusted input — classic SSRF vector); retries via a bounded webhook-retry topic (`10`); source-IP
  allow-listing offered (some providers/tenants require it, e.g. Dropcontact pattern).
  - **Tenant-bound allow-list (G1 — prevents cross-tenant PII egress):** the target host is validated
    against **the delivering job's own tenant's** registered webhook set, resolved **server-side from the
    job's `tenant_id`** — **not** a global host set. Tenant A's enriched PII can only ever be delivered to a
    host tenant A registered. This closes the IDOR/cross-tenant-egress path a global allow-list would open.

## 7. Open items
| ID | Item | Status |
|----|------|--------|
| PX-1 | Egress architecture | ✅ centralized proxy fleet (ADR-0010) |
| PX-2 | SSRF resolver + allow-list spec | ✅ §2 (canonical; `18` references) |
| PX-3 | Regional egress IP plan | drafted; finalize with `19` |

## 8. Reviewer result (`/security-audit` Phase 13 — SSRF priority)
| Check | Result |
|-------|--------|
| Single egress choke guaranteed by network policy (default-deny elsewhere) | PASS |
| No fetch of record/tenant-supplied host without allow-list | PASS |
| DNS-rebinding-safe resolver (resolve→pin→revalidate); redirects re-checked | PASS |
| Metadata/RFC1918/loopback/ULA/CGNAT blocked | PASS |
| HTTPS-only scheme/port allow-list; response limits | PASS |
| Keys injected at proxy; never in worker memory/logs | PASS |
| Outbound webhook URLs treated as untrusted (SSRF-checked) | PASS |

**Gate:** `GATE-PASS` (auto-advance; recorded).
