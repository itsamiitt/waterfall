# 27 — Implementation Slice 05: egress-proxy / SSRF choke (Go)

**Status:** `IMPLEMENTED` (tests green) · **Owner:** Staff Security Engineer · **Last updated:** 2026-07-01
**Builds on:** [`26`](26-Implementation-Slice-04.md) · **Canonical spec:** [`13`](13-Proxy-Management.md), [`18`](18-Security.md) §2 · **Approved by:** human (2026-07-01)

> The #2 security risk (SSRF in the enrichment fetch path) made concrete. All outbound
> calls now flow through a hardened egress client that wraps the Slice-04 key-injection
> seam with an allow-list + a DNS-rebinding-safe dial guard.

## 1. Defenses implemented (`internal/provider/ssrf.go`)
Enforced in order per outbound request:
1. **HTTPS-only** — non-https schemes refused.
2. **FQDN allow-list** — the host must be explicitly permitted (`HostAllowList`); never
   derived from record data. Provider hosts are a global list; tenant webhook hosts are a
   per-tenant list (docs/13 §6) — the caller supplies the right list per purpose.
3. **Dial-time IP guard** — the dialer `Control` hook validates the **actual resolved IP**
   about to be connected to, refusing loopback, RFC1918 + ULA, link-local (incl. cloud
   **metadata `169.254.169.254`**), multicast, unspecified, **CGNAT 100.64/10**, `0.0.0.0/8`,
   and **IPv4-mapped IPv6**. Because the check is on the resolved binary IP at dial time, it
   is immune to **DNS-rebinding** (allowed name → internal IP is still blocked) and to
   IP-literal encoding tricks.
4. **Redirects re-checked** on every hop (scheme + allow-list) and capped.
5. **Key injection at the choke** — `AuthInjector` (Slice 04) sits inside this boundary, so
   the secret exists only on the wire, only for allowed destinations.

Layering: `hostGuard(https + allow-list) → AuthInjector(key) → EgressTransport(IP-guarded dial)`.
An SSRF refusal (`ErrSSRFBlocked`) is classified **BAD_REQUEST** (non-retryable) by adapters.

## 2. Tests (4 new; 57 total) — the SSRF corpus (docs/21)
- **`TestSSRF_IPBlocklistCorpus`**: 17 blocked addresses (metadata, RFC1918, loopback, ULA,
  link-local, CGNAT, `0.0.0.0/8`, IPv4-mapped loopback/RFC1918) all refused; public IPs pass;
  nil fails closed.
- **`TestSSRF_DialControlBlocksInternal`**: a real dial to a loopback server through the
  egress transport is blocked at the IP guard (the rebinding-safe enforcement point).
- **`TestSSRF_HostGuardEnforcesHTTPSAndAllowList`**: http refused, disallowed host refused
  (inner transport never reached), allowed https host passes.
- **`TestSSRF_EgressClientRefusesDisallowedHost`**: the full client refuses the metadata
  endpoint.

## 3. How adapters use it
`adapters.Hunter(base, provider.NewEgressClient(allow, keyResolver))` — the adapter is
unchanged from Slice 04; only the `*http.Client` it's given is now the hardened egress
client. The allow-list is built from the provider's base host; keys resolve from the pool.

## 4. Honestly out of this slice
- **Process/network isolation**: this is an in-process egress *library* enforcing the same
  policy. The production choke is also a **network-level** default-deny egress (only the
  proxy subnet routes to the internet, docs/13/19) — belt-and-suspenders; the library alone
  does not stop code that bypasses it, which is why the network policy exists.
- Per-tenant webhook allow-list is supported by `HostAllowList` but not yet wired (arrives
  with the webhooks-out slice).
- Egress as a separate **service** (its own deployable) rather than a library.
- Fine-grained per-provider egress metrics/audit of blocked attempts (docs/20).

## 5. Reviewer result (`/security-audit` Slice 05)
| Check | Result |
|-------|--------|
| HTTPS-only + FQDN allow-list (host never from record data) | PASS |
| Resolved-IP dial guard (rebinding + encoding safe) | PASS |
| Full internal-range corpus blocked incl. metadata/CGNAT/mapped | PASS |
| Redirects re-checked + capped | PASS |
| Key injection stays inside the choke (Slice 04) | PASS |
| SSRF refusal is non-retryable (BAD_REQUEST) | PASS |
| Network-level default-deny still required (documented, §4) | PASS (honest) |
| `go build/vet/test/gofmt` clean | PASS |

**Gate:** slice `IMPLEMENTED`. The two highest-risk areas (G1 tenant isolation, P2 SSRF) now
both have concrete, tested enforcement in code.
