-- Migration 0012 — provisioning + MFA knob + bulk cancel + cost key_id (doc 03 §2.9, doc 15 Part 1).

-- SEC-5: per-Tenant MFA requirement (tenant_admin knob; default off).
ALTER TABLE tenants ADD COLUMN require_mfa boolean NOT NULL DEFAULT false;

-- SEC-3 / ADR-0021: first-admin (and later) invite tokens. Class T; token_hash is sha256 of the
-- emitted 256-bit token (never plaintext). The accept-invite path is token-authenticated
-- (pre-session) and binds the invite's own tenant_id.
CREATE TABLE tenant_invites (
    id         uuid PRIMARY KEY,
    tenant_id  text NOT NULL REFERENCES tenants(id),
    email      text NOT NULL,
    role       text NOT NULL CHECK (role IN ('tenant_admin', 'tenant_user')),
    token_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tenant_invites_token_idx ON tenant_invites (token_hash);
ALTER TABLE tenant_invites ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_invites FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_invites_tenant_isolation ON tenant_invites
    USING (tenant_id = app_current_tenant())
    WITH CHECK (tenant_id = app_current_tenant());

-- OI-API-4: bulk-job cancellation. Widen the status enum + add a cooperative cancel flag the
-- executors poll between rows/waves.
ALTER TABLE bulk_jobs DROP CONSTRAINT bulk_jobs_status_check;
ALTER TABLE bulk_jobs ADD CONSTRAINT bulk_jobs_status_check
    CHECK (status IN ('queued', 'running', 'succeeded', 'partial', 'failed', 'cancelled'));
ALTER TABLE bulk_jobs ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false;

-- RF-3: key-level cost. Add key_id to the cost rollup grain (partitioned parent → propagates to
-- partitions). Older days keep key_id='' (unattributed); refold from usage_events populates recent
-- days. Cardinality bounded by the same 2y retention + monthly partition drops (doc 03 §4).
ALTER TABLE cost_rollup_1d ADD COLUMN key_id text NOT NULL DEFAULT '';
ALTER TABLE cost_rollup_1d DROP CONSTRAINT cost_rollup_1d_pkey;
ALTER TABLE cost_rollup_1d ADD PRIMARY KEY (tenant_id, provider_id, workflow_key, country, key_id, day);
