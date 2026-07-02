# 36 — Implementation Slice 14: SCRAM-SHA-256 auth + TLS + migration runner (Go)

**Status:** `IMPLEMENTED` (mainline green + **SCRAM/TLS/migration tests passed live on PostgreSQL 17**) · **Owner:** Staff Backend Engineer · **Last updated:** 2026-07-01
**Builds on:** [`35`](35-Implementation-Slice-13.md) · **Canonical spec:** [`12`](12-Secrets-and-Egress.md), [`18`](18-Security.md) · **Approved by:** human (2026-07-01)

> Hardens the stdlib `pg` client for real deployments — it now speaks **SCRAM-SHA-256** (so it
> works against a password-auth Postgres, not just trust) and optional **TLS** — and adds an
> ordered **migration runner** so schema is applied reproducibly instead of by hand. Still
> zero external dependencies.

## 1. SCRAM-SHA-256 — `internal/pg/scram.go`
Full client side of RFC 5802 / RFC 7677 (no channel binding), stdlib-only (`crypto/pbkdf2`,
Go 1.24+): client-first → server-first → `SaltedPassword = PBKDF2(HMAC-SHA256)` →
`ClientProof = ClientKey XOR HMAC(StoredKey, AuthMessage)` → **mutual auth** (the server-final
verifier is checked against the expected `ServerSignature`, constant-time). The username is
SASL-escaped into the message. Wired into the startup handshake as SASL (`Authentication` code
10 → SASLInitialResponse / SASLContinue / SASLResponse / SASLFinal).

**Verified two ways:** a mainline unit test against the **RFC 7677 worked example** (exact
client proof + server verifier, no server needed), and a **live** test authenticating as a
real `scram-sha-256` password role on PostgreSQL 17.10.

## 2. TLS — `internal/pg` (`Config.TLS` / DSN `sslmode`)
Implements the Postgres `SSLRequest` negotiation: send the magic request, and on `S` upgrade
the socket with `crypto/tls` before the startup handshake. `sslmode` in the DSN follows libpq
semantics (`require` → encrypt without cert verification; `verify-ca`/`verify-full` → verify
with the host as `ServerName`). **Live-verified:** connected with `sslmode=require` and
confirmed via `pg_stat_ssl` that the backend sees an encrypted connection.

## 3. Migration runner — `internal/pgmigrate`
`Apply(conn, dir)` creates a `schema_migrations` table, applies each not-yet-applied
`NNNN_*.sql` in filename order, and records it — each file + its version row in **one
transaction** (all-or-nothing). Idempotent: re-runs are no-ops. The two migration files
(`0001`, `0002`) had their own `BEGIN/COMMIT` removed so the runner owns the transaction (they
are still applied atomically by the runner, and directly-applied multi-statement `Exec` remains
a single implicit transaction). **Live-verified:** applies both migrations in order, tables
exist, re-apply returns nothing, `schema_migrations` has 2 rows.

## 4. Tests
Mainline (91 total): `TestSCRAM_RFC7677Vector` (RFC proof + verifier), `TestSCRAM_NonceMismatchRejected`.
Live (`-tags integration`): `TestConn_SCRAM`, `TestConn_TLS`, `TestApply_OrderedAndIdempotent`
— **9 live integration tests now pass** on PostgreSQL 17.10 (SCRAM/TLS gated on a
scram-role + ssl=on cluster, set up by the run doc). Mainline `go build/vet/test/gofmt` clean.

## 5. Honestly out of this slice
- **No SCRAM channel binding** (`SCRAM-SHA-256-PLUS`) — the no-binding mechanism is
  implemented; PLUS (binding the auth to the TLS channel) is future work.
- **No MD5 auth** — SCRAM is the modern default; MD5 (deprecated) is not implemented.
- **`sslmode=require` does not verify the certificate** (matches libpq) — production should use
  `verify-full` with a real CA; the self-signed live test used `require`.
- **The migration runner has no `down`/rollback and no checksum drift detection** — forward-only,
  filename-ordered; a changed already-applied file is not re-detected. Adequate for this
  codebase; a fuller tool (dirty-state, checksums) is future work.

## 6. Reviewer result
| Check | Result |
|-------|--------|
| SCRAM-SHA-256 matches the RFC 7677 vector (proof + mutual-auth verifier) | PASS |
| SCRAM authenticates against a real password role (live) | PASS |
| TLS negotiated; backend connection encrypted per `pg_stat_ssl` (live) | PASS |
| Migration runner: ordered, atomic-per-file, idempotent (live) | PASS |
| Zero new external dependencies (stdlib crypto only) | PASS |
| Mainline `go build/vet/test/gofmt` clean; 9 live integration tests pass | PASS |
| Channel binding, MD5, cert-verify-by-default, down-migrations deferred (§5) | PASS |

**Gate:** slice `IMPLEMENTED`; the pg client is now deployment-grade (SCRAM + TLS) with a
reproducible migration path. Proceeds to the next increment on request.
