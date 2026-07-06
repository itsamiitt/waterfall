# 21. Operator Tenant provisioning via target-Tenant-bound INSERT (no BYPASSRLS)

Date: 2026-07-06

## Status

Accepted

## Context

Customer Tenants and their first `tenant_admin` User must be created somewhere. The dashboard's RLS
model (ADR-0020) deliberately gives operators **read-only** cross-Tenant access: the `tenants` and
`users` policies are `WITH CHECK (id/tenant_id = app_current_tenant())`, and the operator
cross-Tenant policies are `FOR SELECT` only. So an operator Principal (bound to the sentinel
`platform` Tenant) **cannot** insert a row for a *different* Tenant through the normal app role —
by design (doc 05 SEC-3). Until now, Tenant creation was an out-of-band step (a superuser SQL script
/ the dev `dashseed` tool), which is unaudited and off the controlled surface.

We need a first-class, audited provisioning path that does not weaken the G1 isolation guarantees —
specifically, one that does **not** grant the app role `BYPASSRLS` (ADR-0011's core invariant).

## Decision

Provisioning is a dedicated operator-only endpoint (`POST /v1/admin/tenants`, MFA step-up, audited)
whose handler checks the operator's RBAC, then opens **one transaction that binds
`app.current_tenant` to the NEW Tenant's id** (not `platform`). Under that binding the standard
`WITH CHECK (… = app_current_tenant())` policies pass for the new Tenant, so the tx can insert the
`tenants` row, the first `users` row (status `invited`, no password), and a `tenant_invites` row
(hashed one-time token) — all under normal FORCE RLS, no BYPASSRLS. First-admin password setup
happens via a separate token-authenticated public endpoint (`POST /v1/admin/auth/accept-invite`).

The authority to create *any* Tenant lives in the **handler** (operator role + step-up + audit),
not in the DB role; the DB binding is merely scoped to the Tenant being created, so a bug cannot
touch a *different* existing Tenant's rows (the binding is a single id, validated against the slug
CHECK and a not-already-exists check).

## Options considered

- **Target-Tenant-bound INSERT (chosen).** No new DB privilege; the operation is scoped to exactly
  the new Tenant; fully audited; reuses the existing tx helper. Slightly unusual (the handler binds
  a tenant it is provisioning rather than the caller's own), so it is documented here and in doc 05.
- **A BYPASSRLS provisioning role.** Rejected: violates the ADR-0011 "no BYPASSRLS on the app path"
  invariant and widens the blast radius of any provisioning bug to every Tenant.
- **Keep it out-of-band (superuser script / dashseed).** Rejected as the *primary* path: unaudited,
  off the RBAC/MFA surface, and not usable by operators without DB access. Retained only as a dev
  convenience.
- **Self-serve public signup.** Deferred (not chosen for v1): a larger surface (email verification,
  plan selection, abuse controls) that the operator path does not need; can be layered later on the
  same provisioning service.

## Consequences

- Operators get an audited, MFA-gated `POST /v1/admin/tenants` + a token invite for the first admin;
  `dashseed` is demoted to a dev-only convenience.
- The "handler binds the tenant it provisions" pattern is a documented, narrowly-scoped exception to
  the usual "bind the caller's own tenant" rule — enforced to a single validated id, so it cannot
  read or write any other Tenant. The RLS fuzz test still asserts zero cross-Tenant leakage on
  `tenant_invites` and the provisioning path.
- No change to the app DB role's privileges; the G1/ADR-0011 invariants stand.
