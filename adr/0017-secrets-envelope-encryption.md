# ADR 0017 — Secrets at rest: AES-256-GCM envelope encryption behind a pluggable `secrets.Backend`

- **Status:** Accepted (resolves open item **KM-1** / SE-secrets)
- **Date:** 2026-07-02
- **Deciders:** Senior Backend Engineer, Staff Security Engineer, Solutions Architect
- **Phase:** Dashboard P1 · **Source:** `docs/waterfall-dashboard/05-rbac-security.md`

## Context
The dashboard stores four kinds of secret material: **Provider Keys** (1,000+ per Provider across
hundreds of Providers — design target, UNVERIFIED until load-tested), TOTP seeds, webhook signing
secrets, and alert-channel configuration containing credentialed URLs. `docs/18-Security.md §4` forbids
plaintext secrets anywhere — code, logs, error messages, or tables. Open item KM-1 deferred the
secrets-backend choice (Vault vs cloud KMS) to implementation start; this is that decision. Constraints:
the Go backend is stdlib-only (no Vault client, no cloud SDK); the key-lease hot path targets up to 10k
selections/s (UNVERIFIED, P2 load-test gate), so decryption cannot afford a network round-trip; RLS is
an authorization mechanism, not encryption — backups, replicas, and dump files fall outside it.

## Options considered
| Option | Pros | Cons | Key tradeoff surfaced |
|--------|------|------|-----------------------|
| **A. Envelope encryption in Postgres, master key from environment (chosen)** | stdlib-only (`crypto/aes`, `cipher.GCM`, `crypto/hmac`); no new stateful service; DEK unwrap is local — no per-call network hop on the lease path; master-key rotation is a lazy re-wrap of tiny `dek_wrapped` values, never bulk ciphertext | master key custody is **weaker than KMS/HSM**: host-env or memory-dump compromise exposes all DEKs; we own key hygiene and rotation tooling | ops simplicity + hot-path latency vs hardware-grade custody |
| B. HashiCorp Vault now | battle-tested custody, leases, audit devices | a **new stateful HA-critical service** (unseal ceremony, storage backend, its own DR) plus a non-stdlib client — against the repo's ops-simplicity and zero-dependency values, for a single-region deployment | custody strength vs operational footprint; **revisit at multi-region** |
| C. Cloud KMS direct (encrypt/decrypt API per secret access) | HSM-backed custody; no key material in process | a **network call per decrypt on the hot lease path** adds tail latency and makes an external service a hard availability dependency; vendor lock-in contradicts ADR-0015 portability-first; **kept as a master-key wrap option** (KMS wraps the KEK, not each DEK) | latency/availability vs custody |
| D. Plaintext in an RLS-protected table | trivial | **rejected outright: violates `docs/18 §4`.** RLS is authorization, not encryption — plaintext leaks via backups, replicas, `pg_dump`, and any future BYPASSRLS relay role | none; not a viable option |

## Decision
**Envelope encryption, Option A**, implemented in `internal/dash/secrets` behind a pluggable interface:

- `secrets.Backend{ Seal(ctx, kind, plaintext) (EnvelopeID, error); Open(ctx, id) ([]byte, error); Rotate(ctx, id) (EnvelopeID, error) }`
  — consumer-side interface; Vault and AWS Secrets Manager adapters are **designed but deferred** (the
  interface is the adoption; the deployment is the rejection).
- Per-secret **32-byte random DEK**; payload encrypted with **AES-256-GCM**; the DEK itself wrapped by a
  **master key (KEK) supplied via environment** (`DASH_MASTER_KEY`, a keyring of `master_key_id → 32B key`
  so two KEKs can be live during rotation).
- `secret_envelopes` table (migration 0005, Class P per ADR-0020):
  `id uuid PK, kind CHECK IN ('provider_key','totp_seed','webhook_secret','channel_config'),
  master_key_id text, dek_wrapped bytea, nonce bytea, ciphertext bytea, aad_fingerprint bytea,
  created_at, rotated_from uuid NULL`. **No plaintext column exists anywhere in the schema.**
- GCM **AAD binds ciphertext to its row**: AAD = envelope id ‖ kind (swap/splice detection).
  `aad_fingerprint` = HMAC-SHA256(server-side pepper, plaintext) — **keyed, never bare SHA-256** — for
  duplicate detection at import without decryption and without offline brute-force of short vendor keys.
- Consumers store **references, never values**: `provider_keys.secret_envelope_id`,
  `users.mfa_totp_envelope_id`, `alert_channels.config_envelope_id`. Only the secrets backend reads
  `secret_envelopes` (one-owner-per-table); it carries **no tenant RLS policy ever** (ADR-0020).
- A **`Secret` wrapper type redacts `String()` and `MarshalJSON`** so `log/slog`, panics, and API
  serialization cannot leak values. There is **no reveal endpoint** in the `/v1/admin/*` surface — only
  `secret_last4` and a fingerprint prefix identify a Provider Key.
- **Master-key rotation**: mint a new `master_key_id`; `Rotate` re-wraps `dek_wrapped` only (ciphertext
  untouched, `rotated_from` records lineage); a background loop drains old-KEK envelopes; the old KEK is
  retired from the keyring when its envelope count reaches zero. Changing the backend itself
  (`secrets_backend_change`) is approval-gated with quorum + TOTP step-up per migration 0007.

## Rationale
The lease hot path decides this ADR: G3 bounded execution gives every Provider call a strict time
budget, and a per-call network unwrap (Options B/C) spends that budget on infrastructure instead of the
Provider. Envelope encryption keeps `Open` a local AES operation while preserving every property KM-1
demanded — no plaintext at rest, rotation without bulk re-encryption, per-secret blast radius — and the
`secrets.Backend` seam means adopting Vault later is an adapter, not a migration. We chose **hot-path
latency and ops simplicity over hardware-grade custody**, and we state the losing concern plainly:
env-held KEKs are weaker than an HSM. That residual risk is bounded by deployment controls (doc 11:
restricted env exposure, no core dumps in prod) and reversed entirely the day the Vault adapter ships.

## Consequences
- Positive: KM-1 closed; zero plaintext at rest as a **schema-level invariant**, not a code-review hope;
  lease path stays local and fast; G5 provenance-adjacent lineage via `rotated_from`; swap-in path for
  Vault/AWS-SM preserved.
- Negative / accepted costs: env-based KEK custody (stated above); plaintext exists **transiently in
  process memory** during import, key test, and egress injection — Go's GC does not zeroize, so prod
  disables core dumps and every log/JSON path goes through the redacting `Secret` type; zero-reveal
  means lost vendor keys are unrecoverable by design (import UX must say so); the fingerprint pepper is
  itself a secret to manage alongside the keyring.
- Follow-ups / new ADRs triggered: Vault adapter ADR at multi-region (re-opens custody); ADR-0020
  governs the table's RLS class; runbook "master-key rotation" in `docs/waterfall-dashboard/14-runbooks.md`.

## Verification
Unit tests validate AES-256-GCM against **NIST test vectors** and AAD tamper rejection; a secret-scan
test asserts no plaintext or DEK material in logs, JSON output, or error strings; an integration test
proves `secret_envelopes` returns zero rows under any tenant principal (RLS zero-rows, release blocker);
a rotation drill re-wraps a populated table and proves old-KEK retirement; import dedupe is tested via
keyed-fingerprint collision. Hot-path claim (local `Open` sustains 10k selections/s) is UNVERIFIED until
the P2 load test.
